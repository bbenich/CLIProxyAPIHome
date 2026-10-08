package respserver

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
	"github.com/router-for-me/CLIProxyAPIHome/internal/config"
	"github.com/router-for-me/CLIProxyAPIHome/internal/home"
	"github.com/router-for-me/CLIProxyAPIHome/internal/node"
	"github.com/router-for-me/CLIProxyAPIHome/internal/respserver/dispatch"
	resppush "github.com/router-for-me/CLIProxyAPIHome/internal/respserver/push"
	log "github.com/sirupsen/logrus"
)

type fingerprintCancellationStarter interface {
	BeginFingerprintCancellationForLifetime(context.Context, cluster.ConnectionLifetime) (int64, error)
}

type clusterHandler interface {
	ClassifyConnection(context.Context, string) (cluster.ConnectionLifetime, error)
	SubscribeMembership(context.Context, string, string, int, int64, bool, string) (cluster.ConnectionLifetime, error)
	RefreshCPALiveness(context.Context, cluster.ConnectionLifetime) error
	UpdateClientCount(context.Context, int) error
	Handle(context.Context, []string, string) ([]byte, error)
	RequestClientCertificate(context.Context, string, string, []byte) ([]byte, error)
}

type Server struct {
	addr                    string
	runtime                 *home.Runtime
	registry                *dispatch.Registry
	cluster                 clusterHandler
	fingerprints            *FingerprintRegistry
	inFlightStore           dispatch.InFlightSnapshotStore
	concurrencyReleaseStore dispatch.ConcurrencyReleaseStore
	inFlightLimits          atomic.Pointer[cluster.InFlightLimits]
}

const (
	clusterSubscriptionUpdateInterval = 30 * time.Second
	subscriptionHeartbeatInterval     = time.Second
)

const (
	respAuthSourceNone = "none"
	respAuthSourceMTLS = "mtls"
)

const (
	// respUnauthenticatedIdleTimeout bounds how long a connection without mTLS
	// may stay silent between commands. Authenticated CPA connections are pooled
	// and subscribed, so they intentionally have no idle deadline.
	respUnauthenticatedIdleTimeout = 30 * time.Second
	// respFrameReadTimeout bounds how long a single command frame may take to
	// arrive once its first byte has been received.
	respFrameReadTimeout = 30 * time.Second
	// respWriteTimeout bounds every reply or subscription write.
	respWriteTimeout = 30 * time.Second

	// respMaxLineBytes bounds RESP header and simple-string lines.
	respMaxLineBytes = 64 * 1024
	// respBulkReadChunkBytes bounds how far a bulk buffer grows ahead of the bytes received.
	respBulkReadChunkBytes = 64 * 1024
	// respArgsPreallocLimit bounds argument slice preallocation from wire counts.
	respArgsPreallocLimit = 16

	// Before mTLS only CERTIFICATE REQUEST (5 arguments with a CSR PEM) is useful.
	respUnauthenticatedMaxArgs       = 8
	respUnauthenticatedMaxFrameBytes = 64 * 1024
	// Authenticated CPA frames carry request logs with full bodies, in-flight
	// snapshot parts and usage batches. The cap matches the Redis default
	// proto-max-bulk-len and is applied to the whole frame.
	respAuthenticatedMaxArgs       = 4096
	respAuthenticatedMaxFrameBytes = 512 * 1024 * 1024
)

var errRESPProtocol = errors.New("protocol error")

// respFrameLimits bounds a single inbound RESP command frame.
type respFrameLimits struct {
	maxArgs       int
	maxFrameBytes int
}

func respFrameLimitsFor(authenticated bool) respFrameLimits {
	if authenticated {
		return respFrameLimits{maxArgs: respAuthenticatedMaxArgs, maxFrameBytes: respAuthenticatedMaxFrameBytes}
	}
	return respFrameLimits{maxArgs: respUnauthenticatedMaxArgs, maxFrameBytes: respUnauthenticatedMaxFrameBytes}
}

// New creates a new.
func New(addr string, runtime *home.Runtime) *Server {
	if errSanitize := resppush.SanitizeUsageLog(); errSanitize != nil {
		log.Errorf("usage log sanitization error: %v", errSanitize)
	}
	server := &Server{
		addr:         strings.TrimSpace(addr),
		runtime:      runtime,
		registry:     buildRegistry(),
		fingerprints: NewFingerprintRegistry(),
	}
	limits := cluster.DefaultInFlightLimits()
	server.inFlightLimits.Store(&limits)
	if runtime != nil {
		// Every config apply path publishes the new config after applying it, so
		// re-check allow-host for existing connections on each publish.
		runtime.SubscribeConfigYAML(func([]byte) error {
			server.enforceAllowHost()
			return nil
		})
	}
	return server
}

// enforceAllowHost closes tracked connections whose remote IP is no longer allowed.
func (s *Server) enforceAllowHost() {
	if s == nil || s.runtime == nil || s.fingerprints == nil {
		return
	}
	cfg := s.runtime.Config()
	if cfg == nil || len(cfg.AllowHost) == 0 {
		return
	}
	allowHosts := append([]string(nil), cfg.AllowHost...)
	closed := s.fingerprints.CloseConnectionsWhere(func(conn net.Conn) bool {
		clientIP, _ := resolveRemoteIP(conn.RemoteAddr())
		return !isClientHostAllowed(clientIP, allowHosts)
	})
	if closed > 0 {
		log.WithField("connections", closed).Warn("closed resp connections from hosts no longer allowed")
	}
}

// SetInFlightSnapshotStore sets the repository used to ingest in-flight observation frames.
func (s *Server) SetInFlightSnapshotStore(store dispatch.InFlightSnapshotStore) {
	if s == nil {
		return
	}
	s.inFlightStore = store
}

// SetConcurrencyReleaseStore sets the repository used to apply cumulative limiter releases.
func (s *Server) SetConcurrencyReleaseStore(store dispatch.ConcurrencyReleaseStore) {
	if s == nil {
		return
	}
	s.concurrencyReleaseStore = store
}

// ApplyInFlightConfig validates and atomically applies in-flight observation limits.
func (s *Server) ApplyInFlightConfig(cfg config.CredentialInFlightConfig) error {
	if s == nil {
		return fmt.Errorf("RESP server is nil")
	}
	limits, errLimits := cluster.InFlightLimitsFromConfig(cfg)
	if errLimits != nil {
		return errLimits
	}
	s.inFlightLimits.Store(&limits)
	return nil
}

func (s *Server) currentInFlightLimits() cluster.InFlightLimits {
	if s == nil {
		return cluster.DefaultInFlightLimits()
	}
	limits := s.inFlightLimits.Load()
	if limits == nil {
		return cluster.DefaultInFlightLimits()
	}
	return *limits
}

// FenceFingerprint drains an exact lifetime before persisting its acknowledgement.
func (s *Server) FenceFingerprint(ctx context.Context, lifetime cluster.ConnectionLifetime, revision int64, acknowledge func() error) error {
	if s == nil || s.fingerprints == nil {
		return fmt.Errorf("fingerprint registry is unavailable")
	}
	errFence := s.fingerprints.FenceAndAcknowledge(ctx, lifetime, revision, acknowledge)
	if strings.TrimSpace(lifetime.Fingerprint) != "" {
		node.GlobalRegistry().UpdateFingerprintState(lifetime.Fingerprint, "", "", 0, 0, s.fingerprints.LatestFenceRevision(lifetime))
	}
	return errFence
}

// SetClusterHandler sets a cluster handler.
func (s *Server) SetClusterHandler(handler clusterHandler) {
	if s == nil {
		return
	}
	s.cluster = handler
}

func (s *Server) syncClusterClientCount(ctx context.Context) {
	if s == nil || s.cluster == nil {
		return
	}
	if errSync := s.cluster.UpdateClientCount(ctx, node.GlobalRegistry().TotalCount()); errSync != nil {
		log.Warnf("failed to sync cluster client count: %v", errSync)
	}
}

func (s *Server) startSubscriptionUpdates(ctx context.Context, tracked *TrackedConnection, writer *safeWriter) context.CancelFunc {
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)
	if s == nil || tracked == nil || writer == nil {
		return cancel
	}

	go func() {
		heartbeatTicker := time.NewTicker(subscriptionHeartbeatInterval)
		defer heartbeatTicker.Stop()

		for {
			select {
			case <-runCtx.Done():
				return
			case <-heartbeatTicker.C:
				lifetime := tracked.Lifetime()
				if errLiveness := s.refreshSubscriptionLiveness(runCtx, lifetime); errLiveness != nil {
					log.Warnf("failed to refresh subscription liveness: %v", errLiveness)
					if s.fingerprints != nil {
						if errFence := s.fingerprints.Fence(context.WithoutCancel(runCtx), lifetime, s.fingerprints.LatestFenceRevision(lifetime)); errFence != nil {
							log.Warnf("failed to fence subscription after liveness failure: %v", errFence)
						}
					}
					cancel()
					return
				}
				if errSend := writer.WriteDispatchReply(subscriptionPong([]byte{})); errSend != nil {
					log.Warnf("failed to publish subscription heartbeat: %v", errSend)
					cancel()
					return
				}
			}
		}
	}()

	if s.cluster != nil {
		go func() {
			ticker := time.NewTicker(clusterSubscriptionUpdateInterval)
			defer ticker.Stop()

			for {
				select {
				case <-runCtx.Done():
					return
				case <-ticker.C:
					if errSend := s.writeClusterSubscriptionUpdate(runCtx, writer); errSend != nil {
						log.Warnf("failed to publish cluster update to subscriber: %v", errSend)
						cancel()
						return
					}
				}
			}
		}()
	}

	return cancel
}

func (s *Server) refreshSubscriptionLiveness(ctx context.Context, lifetime cluster.ConnectionLifetime) error {
	if s == nil || s.cluster == nil {
		return fmt.Errorf("cluster lifecycle unavailable")
	}
	return s.cluster.RefreshCPALiveness(ctx, lifetime)
}

func (s *Server) writeClusterSubscriptionUpdate(ctx context.Context, writer *safeWriter) error {
	if s == nil || s.cluster == nil || writer == nil {
		return nil
	}
	s.syncClusterClientCount(ctx)
	payload, errPayload := s.cluster.Handle(ctx, []string{clusterCommand, "NODES"}, "")
	if errPayload != nil {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		log.Warnf("failed to build cluster update for subscriber: %v", errPayload)
		return nil
	}
	if len(payload) == 0 {
		return nil
	}
	return writer.WriteDispatchReply(subscriptionMessage(clusterSubscriptionChannel, payload))
}

func buildOrWriteDispatchReply(writer *safeWriter, reply dispatch.Reply) error {
	if reply.PreWriteError != nil {
		return reply.PreWriteError
	}
	if writer == nil {
		return net.ErrClosed
	}
	return writer.WriteDispatchReply(reply)
}

func writeDispatchReplyWithFence(serverCtx context.Context, writer *safeWriter, reply dispatch.Reply, connEnv *dispatch.ConnEnv) error {
	errWrite := buildOrWriteDispatchReply(writer, reply)
	if errWrite != nil && reply.AccountedAdmission {
		fenceAccountedDispatch(serverCtx, connEnv)
	}
	return errWrite
}

func fenceAccountedDispatch(serverCtx context.Context, connEnv *dispatch.ConnEnv) {
	if connEnv == nil {
		return
	}
	if connEnv.FenceFingerprint != nil {
		fenceCtx, cancelFence := context.WithTimeout(context.WithoutCancel(serverCtx), 5*time.Second)
		if errFence := connEnv.FenceFingerprint(fenceCtx, "accounted_dispatch_delivery_failed"); errFence != nil {
			log.Errorf("failed to fence ambiguous concurrency admission: %v", errFence)
		}
		cancelFence()
	}
	if connEnv.CloseLocalFingerprint != nil {
		connEnv.CloseLocalFingerprint()
	}
}

func (s *Server) writeNoAuth(writer *safeWriter) {
	if writer == nil {
		return
	}
	_ = writer.WriteRedisError("NOAUTH Authentication required.")
}

func isRESPAuthenticated(source string) bool {
	source = strings.TrimSpace(source)
	return source == respAuthSourceMTLS
}

func isMTLSAuthenticated(conn net.Conn) bool {
	stater, ok := conn.(interface{ ConnectionState() tls.ConnectionState })
	if !ok {
		return false
	}
	state := stater.ConnectionState()
	return len(state.PeerCertificates) > 0 && len(state.VerifiedChains) > 0
}

func peerCertificateNodeID(conn net.Conn) string {
	stater, ok := conn.(interface{ ConnectionState() tls.ConnectionState })
	if !ok {
		return ""
	}
	state := stater.ConnectionState()
	if len(state.PeerCertificates) == 0 || state.PeerCertificates[0] == nil {
		return ""
	}
	return strings.TrimSpace(state.PeerCertificates[0].Subject.CommonName)
}

func peerCertificateFingerprint(conn net.Conn) string {
	stater, ok := conn.(interface{ ConnectionState() tls.ConnectionState })
	if !ok {
		return ""
	}
	state := stater.ConnectionState()
	if len(state.PeerCertificates) == 0 || state.PeerCertificates[0] == nil || len(state.PeerCertificates[0].Raw) == 0 {
		return ""
	}
	sum := sha256.Sum256(state.PeerCertificates[0].Raw)
	return hex.EncodeToString(sum[:])
}

func subscriptionMessage(channel string, payload []byte) dispatch.Reply {
	return dispatch.Array(
		dispatch.BulkString([]byte("message")),
		dispatch.BulkString([]byte(channel)),
		dispatch.BulkString(payload),
	)
}

func subscriptionPong(payload []byte) dispatch.Reply {
	if payload == nil {
		payload = []byte{}
	}
	return dispatch.Array(
		dispatch.BulkString([]byte("pong")),
		dispatch.BulkString(payload),
	)
}

// HandleConn handles handle conn.
func (s *Server) HandleConn(ctx context.Context, conn net.Conn) {
	// Validate request inputs before mutating persisted state.
	if s == nil || conn == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			log.WithField("panic", recovered).Errorf("resp connection handler panic\n%s", debug.Stack())
			if errClose := conn.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
				log.Errorf("resp panicked connection close error: %v", errClose)
			}
		}
	}()

	clientIP, _ := resolveRemoteIP(conn.RemoteAddr())
	if s.runtime != nil {
		if cfg := s.runtime.Config(); cfg != nil && !isClientHostAllowed(clientIP, cfg.AllowHost) {
			log.Warnf("resp connection rejected from disallowed host %s", clientIP)
			if errClose := conn.Close(); errClose != nil {
				log.Errorf("resp disallowed connection close error: %v", errClose)
			}
			return
		}
	}
	authSource := respAuthSourceNone
	clientNodeID := ""
	clientCertificateFingerprint := ""
	if isMTLSAuthenticated(conn) {
		authSource = respAuthSourceMTLS
		clientNodeID = peerCertificateNodeID(conn)
		clientCertificateFingerprint = peerCertificateFingerprint(conn)
	}
	reader := bufio.NewReader(conn)
	writer := newSafeWriter(conn)
	frameLimits := respFrameLimitsFor(isRESPAuthenticated(authSource))
	connectionLifetime := cluster.ConnectionLifetime{
		Fingerprint: clientCertificateFingerprint,
	}
	if s.cluster != nil && clientCertificateFingerprint != "" {
		classifiedLifetime, errClassify := s.cluster.ClassifyConnection(ctx, clientCertificateFingerprint)
		if errClassify != nil {
			log.Warnf("failed to classify RESP connection membership: %v", errClassify)
			if errClose := conn.Close(); errClose != nil {
				log.Errorf("resp membership classification connection close error: %v", errClose)
			}
			return
		}
		connectionLifetime = classifiedLifetime
	}
	tracked, errAccept := s.fingerprints.Accept(ctx, conn, connectionLifetime)
	if errAccept != nil {
		log.Warnf("failed to track RESP connection: %v", errAccept)
		if errClose := conn.Close(); errClose != nil {
			log.Errorf("resp tracking rejection connection close error: %v", errClose)
		}
		return
	}
	connectionCtx := tracked.Context()
	if clientCertificateFingerprint != "" {
		node.GlobalRegistry().UpdateFingerprintState(clientCertificateFingerprint, clientNodeID, clientIP, 1, 0, s.fingerprints.LatestFenceRevision(connectionLifetime))
	}
	connectedAt := time.Now()
	addedNode := false
	var unsubscribeConfig func()
	var cancelSubscriptionUpdates context.CancelFunc
	defer func() {
		if cancelSubscriptionUpdates != nil {
			cancelSubscriptionUpdates()
			cancelSubscriptionUpdates = nil
		}
		if unsubscribeConfig != nil {
			unsubscribeConfig()
			unsubscribeConfig = nil
		}
		if addedNode {
			if clientCertificateFingerprint != "" {
				node.GlobalRegistry().UpdateFingerprintSubscription(clientCertificateFingerprint, clientNodeID, clientIP, -1)
			} else {
				node.GlobalRegistry().RemoveWithNodeID(clientIP, clientNodeID)
			}
			s.syncClusterClientCount(ctx)
			addedNode = false
		}
		if clientCertificateFingerprint != "" {
			node.GlobalRegistry().UpdateFingerprintState(clientCertificateFingerprint, clientNodeID, clientIP, -1, 0, s.fingerprints.LatestFenceRevision(tracked.Lifetime()))
		}
		if errClose := tracked.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
			log.Errorf("resp connection close error: %v", errClose)
		}
	}()

	var pendingConfigSubscriptionReady chan struct{}
	var pendingConfigSubscriptionAborted chan struct{}
	connEnv := &dispatch.ConnEnv{
		AttachSubscriptionLifetimeFunc: tracked.AttachSubscriptionLifetime,
		FenceFingerprint: func(fenceCtx context.Context, _ string) error {
			starter, ok := s.cluster.(fingerprintCancellationStarter)
			if !ok || starter == nil {
				return fmt.Errorf("cluster fingerprint cancellation is unavailable")
			}
			_, errBegin := starter.BeginFingerprintCancellationForLifetime(fenceCtx, connectionLifetime)
			return errBegin
		},
		CloseLocalFingerprint: func() {
			if s.fingerprints == nil {
				return
			}
			if errFence := s.fingerprints.Fence(context.WithoutCancel(ctx), connectionLifetime, s.fingerprints.LatestFenceRevision(connectionLifetime)); errFence != nil {
				log.Errorf("failed to locally fence ambiguous concurrency admission: %v", errFence)
			}
		},
		SubscribeConfigYAML: func() (int64, error) {
			if s.runtime == nil {
				return 0, fmt.Errorf("runtime not ready")
			}
			if unsubscribeConfig != nil {
				return 1, nil
			}
			if !addedNode {
				if clientCertificateFingerprint != "" {
					node.GlobalRegistry().UpdateFingerprintSubscription(clientCertificateFingerprint, clientNodeID, clientIP, 1)
				} else {
					node.GlobalRegistry().AddWithNodeID(clientIP, clientNodeID, connectedAt)
				}
				s.syncClusterClientCount(ctx)
				addedNode = true
			}
			ready := make(chan struct{})
			aborted := make(chan struct{})
			pendingConfigSubscriptionReady = ready
			pendingConfigSubscriptionAborted = aborted
			delivery := newConfigSubscriptionDelivery(connectionCtx, writer, ready, aborted)
			unsubscribeConfig = s.runtime.SubscribeConfigYAML(delivery.Write)
			return 1, nil
		},
		UnsubscribeConfigYAML: func() (int64, error) {
			if unsubscribeConfig == nil {
				return 0, nil
			}
			if cancelSubscriptionUpdates != nil {
				cancelSubscriptionUpdates()
				cancelSubscriptionUpdates = nil
			}
			unsubscribeConfig()
			unsubscribeConfig = nil
			if addedNode {
				if clientCertificateFingerprint != "" {
					node.GlobalRegistry().UpdateFingerprintSubscription(clientCertificateFingerprint, clientNodeID, clientIP, -1)
				} else {
					node.GlobalRegistry().RemoveWithNodeID(clientIP, clientNodeID)
				}
				s.syncClusterClientCount(ctx)
				addedNode = false
			}
			return 0, nil
		},
		IsSubscribed: func() bool {
			return unsubscribeConfig != nil
		},
	}
	if s.cluster != nil && clientCertificateFingerprint != "" {
		connEnv.SubscribeMembership = func(subscriptionCtx context.Context, protocolVersion int, lifecycleConfigRevision int64, takeover bool, instanceID string) (cluster.ConnectionLifetime, error) {
			return s.cluster.SubscribeMembership(subscriptionCtx, clientCertificateFingerprint, clientNodeID, protocolVersion, lifecycleConfigRevision, takeover, instanceID)
		}
	}

	for {
		pendingConfigSubscriptionReady = nil
		pendingConfigSubscriptionAborted = nil
		if errDeadline := armRESPIdleReadDeadline(conn, isRESPAuthenticated(authSource)); errDeadline != nil {
			return
		}
		args, errRead := readRESPArray(reader, frameLimits, func() error {
			return conn.SetReadDeadline(time.Now().Add(respFrameReadTimeout))
		})
		if errRead != nil {
			if errors.Is(errRead, errRESPProtocol) {
				_ = writer.WriteRedisError("ERR " + errRead.Error())
			}
			return
		}

		finishHandler, errBeginHandler := tracked.BeginHandler()
		if errBeginHandler != nil {
			log.Warnf("failed to begin RESP handler: %v", errBeginHandler)
			return
		}
		if clientCertificateFingerprint != "" {
			node.GlobalRegistry().UpdateFingerprintState(clientCertificateFingerprint, clientNodeID, clientIP, 0, 1, s.fingerprints.LatestFenceRevision(tracked.Lifetime()))
		}

		var reply dispatch.Reply
		hasReply := false
		closeConnection := false
		var livenessFailure *cluster.ConnectionLifetime
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					log.WithField("panic", recovered).Errorf("resp command handler panic\n%s", debug.Stack())
					hasReply = false
					closeConnection = true
				}
			}()
			if len(args) == 0 {
				_ = writer.WriteRedisError("ERR empty command")
				return
			}
			cmd := strings.ToUpper(strings.TrimSpace(args[0]))
			if cmd == "CERTIFICATE" {
				if authSource != respAuthSourceNone {
					_ = writer.WriteRedisError("ERR certificate request is only allowed before authentication")
					return
				}
				if s.cluster == nil {
					_ = writer.WriteRedisError("ERR cluster disabled")
					return
				}
				if len(args) != 5 || !strings.EqualFold(strings.TrimSpace(args[1]), "REQUEST") {
					_ = writer.WriteRedisError("ERR wrong number of arguments for 'certificate request' command")
					return
				}
				payload, errCertificate := s.cluster.RequestClientCertificate(connectionCtx, args[2], args[3], []byte(args[4]))
				if errCertificate != nil {
					_ = writer.WriteRedisError("ERR " + errCertificate.Error())
					return
				}
				_ = writer.WriteRedisBulkString(payload)
				return
			}
			if cmd == clusterCommand {
				if !isRESPAuthenticated(authSource) {
					s.writeNoAuth(writer)
					return
				}
				if s.cluster == nil {
					_ = writer.WriteRedisError("ERR cluster disabled")
					return
				}
				payload, errCluster := s.cluster.Handle(connectionCtx, args, clientIP)
				if errCluster != nil {
					_ = writer.WriteRedisError(cluster.FormatRESPError(errCluster))
					return
				}
				_ = writer.WriteRedisBulkString(payload)
				return
			}
			if cmd != "AUTH" && !isRESPAuthenticated(authSource) {
				s.writeNoAuth(writer)
				return
			}
			if cmd == "PING" {
				if unsubscribeConfig != nil {
					lifetime := tracked.Lifetime()
					if errLiveness := s.refreshSubscriptionLiveness(connectionCtx, lifetime); errLiveness != nil {
						log.Warnf("failed to refresh subscription liveness: %v", errLiveness)
						livenessFailure = &lifetime
						closeConnection = true
						return
					}
				}
				switch len(args) {
				case 1:
					if unsubscribeConfig != nil {
						_ = writer.WriteDispatchReply(subscriptionPong([]byte{}))
					} else {
						_ = writer.WriteRedisSimpleString("PONG")
					}
				case 2:
					if unsubscribeConfig != nil {
						_ = writer.WriteDispatchReply(subscriptionPong([]byte(args[1])))
					} else {
						_ = writer.WriteRedisBulkString([]byte(args[1]))
					}
				default:
					_ = writer.WriteRedisError("ERR wrong number of arguments for 'ping' command")
				}
				return
			}
			if cmd == "AUTH" {
				_ = writer.WriteRedisError("ERR RESP AUTH disabled; use mTLS")
				return
			}
			reply = dispatch.Err("registry not ready")
			if s.registry != nil {
				reply = s.registry.Execute(connectionCtx, dispatch.Env{
					Runtime:                      s.runtime,
					ClientIP:                     clientIP,
					NodeID:                       clientNodeID,
					ClientCertificateFingerprint: clientCertificateFingerprint,
					ConnectionLifetime:           connectionLifetime,
					Conn:                         connEnv,
					InFlightStore:                s.inFlightStore,
					InFlightLimits:               s.currentInFlightLimits(),
					ConcurrencyReleaseStore:      s.concurrencyReleaseStore,
				}, args)
			}
			hasReply = true
		}()
		finishHandler()
		if clientCertificateFingerprint != "" {
			node.GlobalRegistry().UpdateFingerprintState(clientCertificateFingerprint, clientNodeID, clientIP, 0, -1, s.fingerprints.LatestFenceRevision(tracked.Lifetime()))
		}
		if livenessFailure != nil {
			if errFence := s.fingerprints.Fence(context.WithoutCancel(connectionCtx), *livenessFailure, s.fingerprints.LatestFenceRevision(*livenessFailure)); errFence != nil {
				log.Warnf("failed to fence subscription after client PING liveness failure: %v", errFence)
			}
		}
		if closeConnection {
			return
		}
		if !hasReply {
			continue
		}
		if reply.SubscriptionLifetime != nil {
			if errAttach := connEnv.AttachSubscriptionLifetime(*reply.SubscriptionLifetime); errAttach != nil {
				if pendingConfigSubscriptionReady != nil {
					close(pendingConfigSubscriptionAborted)
					close(pendingConfigSubscriptionReady)
				}
				log.Warnf("failed to attach RESP subscription lifetime: %v", errAttach)
				return
			}
		}
		if errWrite := writeDispatchReplyWithFence(ctx, writer, reply, connEnv); errWrite != nil {
			if pendingConfigSubscriptionReady != nil {
				close(pendingConfigSubscriptionAborted)
				close(pendingConfigSubscriptionReady)
			}
			log.Errorf("resp write reply error: %v", errWrite)
			return
		}
		if pendingConfigSubscriptionReady != nil {
			payload, errConfig := s.runtime.ReadConfigYAMLContext(connectionCtx)
			if errConfig != nil {
				close(pendingConfigSubscriptionAborted)
				close(pendingConfigSubscriptionReady)
				log.Warnf("failed to publish initial config snapshot: %v", errConfig)
				return
			}
			if errInitial := writer.WriteDispatchReply(subscriptionMessage(configSubscriptionChannel, payload)); errInitial != nil {
				close(pendingConfigSubscriptionAborted)
				close(pendingConfigSubscriptionReady)
				log.Warnf("failed to publish initial config snapshot: %v", errInitial)
				return
			}
			close(pendingConfigSubscriptionReady)
			cancelSubscriptionUpdates = s.startSubscriptionUpdates(connectionCtx, tracked, writer)
		}
	}
}

// armRESPIdleReadDeadline sets the read deadline used while waiting for the next frame.
// Unauthenticated connections get an idle deadline; authenticated CPA connections
// are pooled or subscribed and may legitimately stay idle, so theirs is cleared.
func armRESPIdleReadDeadline(conn net.Conn, authenticated bool) error {
	if authenticated {
		return conn.SetReadDeadline(time.Time{})
	}
	return conn.SetReadDeadline(time.Now().Add(respUnauthenticatedIdleTimeout))
}

// readRESPArray reads a resp array within limits. onFrameStart runs once the
// first byte of the frame has arrived.
func readRESPArray(reader *bufio.Reader, limits respFrameLimits, onFrameStart func() error) ([]string, error) {
	// Decode the wire frame before dispatching command handling.
	prefix, errRead := reader.ReadByte()
	if errRead != nil {
		return nil, errRead
	}
	if onFrameStart != nil {
		if errStart := onFrameStart(); errStart != nil {
			return nil, errStart
		}
	}
	if prefix != '*' {
		return nil, errRESPProtocol
	}
	line, errLine := readRESPLine(reader)
	if errLine != nil {
		return nil, errLine
	}
	count, errAtoi := strconv.Atoi(line)
	if errAtoi != nil || count < 0 {
		return nil, fmt.Errorf("%w: invalid multibulk length", errRESPProtocol)
	}
	if count > limits.maxArgs {
		return nil, fmt.Errorf("%w: too many arguments", errRESPProtocol)
	}
	args := make([]string, 0, min(count, respArgsPreallocLimit))
	remaining := limits.maxFrameBytes
	for i := 0; i < count; i++ {
		value, errValue := readRESPString(reader, remaining)
		if errValue != nil {
			return nil, errValue
		}
		remaining -= len(value)
		args = append(args, value)
	}
	return args, nil
}

// readRESPString reads a resp string of at most maxLength bytes.
func readRESPString(reader *bufio.Reader, maxLength int) (string, error) {
	prefix, errRead := reader.ReadByte()
	if errRead != nil {
		return "", errRead
	}
	switch prefix {
	case '$':
		return readRESPBulkString(reader, maxLength)
	case '+', ':':
		line, errLine := readRESPLine(reader)
		if errLine != nil {
			return "", errLine
		}
		if len(line) > maxLength {
			return "", fmt.Errorf("%w: frame too large", errRESPProtocol)
		}
		return line, nil
	default:
		return "", errRESPProtocol
	}
}

// readRESPBulkString reads a resp bulk string of at most maxLength bytes.
func readRESPBulkString(reader *bufio.Reader, maxLength int) (string, error) {
	line, errLine := readRESPLine(reader)
	if errLine != nil {
		return "", errLine
	}
	length, errAtoi := strconv.Atoi(line)
	if errAtoi != nil {
		return "", fmt.Errorf("%w: invalid bulk length", errRESPProtocol)
	}
	if length < 0 {
		return "", nil
	}
	if length > maxLength {
		return "", fmt.Errorf("%w: invalid bulk length", errRESPProtocol)
	}
	// Grow the buffer only as bytes arrive so a declared length cannot reserve
	// memory ahead of the payload actually received.
	buf := make([]byte, 0, min(length, respBulkReadChunkBytes))
	for len(buf) < length {
		next := min(length-len(buf), respBulkReadChunkBytes)
		buf = slices.Grow(buf, next)
		count, errRead := io.ReadFull(reader, buf[len(buf):len(buf)+next])
		buf = buf[:len(buf)+count]
		if errRead != nil {
			return "", unexpectedEOF(errRead)
		}
	}
	var terminator [2]byte
	if _, errRead := io.ReadFull(reader, terminator[:]); errRead != nil {
		return "", unexpectedEOF(errRead)
	}
	if terminator[0] != '\r' || terminator[1] != '\n' {
		return "", errRESPProtocol
	}
	return string(buf), nil
}

func unexpectedEOF(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// readRESPLine reads a resp line of at most respMaxLineBytes bytes.
func readRESPLine(reader *bufio.Reader) (string, error) {
	var line []byte
	for {
		chunk, errRead := reader.ReadSlice('\n')
		if len(line)+len(chunk) > respMaxLineBytes {
			return "", fmt.Errorf("%w: line too long", errRESPProtocol)
		}
		line = append(line, chunk...)
		if errRead == nil {
			break
		}
		if !errors.Is(errRead, bufio.ErrBufferFull) {
			return "", errRead
		}
	}
	line = bytes.TrimSuffix(line, []byte("\n"))
	line = bytes.TrimSuffix(line, []byte("\r"))
	return string(line), nil
}

// writeRedisSimpleString writes a redis simple string.
func writeRedisSimpleString(writer respWriter, value string) error {
	if writer == nil {
		return net.ErrClosed
	}
	_, errWrite := writer.WriteString("+" + value + "\r\n")
	return errWrite
}

// writeRedisError writes a redis error.
func writeRedisError(writer respWriter, message string) error {
	if writer == nil {
		return net.ErrClosed
	}
	_, errWrite := writer.WriteString("-" + message + "\r\n")
	return errWrite
}

// writeRedisNilBulkString writes a redis nil bulk string.
func writeRedisNilBulkString(writer respWriter) error {
	if writer == nil {
		return net.ErrClosed
	}
	_, errWrite := writer.WriteString("$-1\r\n")
	return errWrite
}

// writeRedisBulkString writes a redis bulk string.
func writeRedisBulkString(writer respWriter, payload []byte) error {
	if writer == nil {
		return net.ErrClosed
	}
	if payload == nil {
		return writeRedisNilBulkString(writer)
	}
	if _, errWrite := writer.WriteString("$" + strconv.Itoa(len(payload)) + "\r\n"); errWrite != nil {
		return errWrite
	}
	if _, errWrite := writer.Write(payload); errWrite != nil {
		return errWrite
	}
	_, errWrite := writer.WriteString("\r\n")
	return errWrite
}

// writeRedisInteger writes a redis integer.
func writeRedisInteger(writer respWriter, value int64) error {
	if writer == nil {
		return net.ErrClosed
	}
	_, errWrite := writer.WriteString(":" + strconv.FormatInt(value, 10) + "\r\n")
	return errWrite
}
