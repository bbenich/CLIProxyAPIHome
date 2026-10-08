package dispatch

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
)

func TestControlledLifetimeGateCoversAllMutatingCommands(t *testing.T) {
	registry := NewRegistry()
	reached := false
	handler := func(context.Context, Env, []string) Reply {
		reached = true
		return SimpleString("OK")
	}
	if errRegister := registry.RegisterDirect("LPRUSH", "usage", handler); errRegister != nil {
		t.Fatal(errRegister)
	}
	if errRegister := registry.SetDirectDefault("SETNX", handler); errRegister != nil {
		t.Fatal(errRegister)
	}

	for _, args := range [][]string{
		{"LPRUSH", "usage", `{"type":"usage"}`},
		{"setnx", "key", "value"},
	} {
		reached = false
		reply := registry.Execute(context.Background(), Env{ConnectionLifetime: cluster.ConnectionLifetime{Fingerprint: "fp"}}, args)
		if reached {
			t.Fatalf("%s reached its handler without a controlled lifetime", args[0])
		}
		if reply.Kind != ReplyKindRedisError || reply.RedisError != "ERR controlled connection required" {
			t.Fatalf("%s reply = %#v, want controlled connection error", args[0], reply)
		}

		reached = false
		reply = registry.Execute(context.Background(), Env{ConnectionLifetime: cluster.ConnectionLifetime{Fingerprint: "fp", Controlled: true}}, args)
		if !reached || reply.SimpleString != "OK" {
			t.Fatalf("%s reply on controlled lifetime = %#v, want handler reply", args[0], reply)
		}
	}
}
