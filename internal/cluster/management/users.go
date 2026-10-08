package management

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type optionalJSONFloat struct {
	set   bool
	clear bool
	value float64
}

func (o *optionalJSONFloat) UnmarshalJSON(data []byte) error {
	o.set = true
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "null" {
		o.clear = true
		return nil
	}
	var value float64
	if errUnmarshal := json.Unmarshal(data, &value); errUnmarshal != nil {
		return errUnmarshal
	}
	o.value = value
	return nil
}

type userWriteRequest struct {
	Username         *string           `json:"username"`
	UserName         *string           `json:"user_name"`
	UserNameDash     *string           `json:"user-name"`
	Password         *string           `json:"password"`
	Credits          *float64          `json:"credits"`
	CreditsUnlimited *bool             `json:"credits_unlimited"`
	Timezone         *string           `json:"timezone"`
	Limit5h          optionalJSONFloat `json:"limit_5h_credits"`
	WindowMode5h     *string           `json:"window_mode_5h"`
	Limit1d          optionalJSONFloat `json:"limit_1d_credits"`
	WindowMode1d     *string           `json:"window_mode_1d"`
	Limit7d          optionalJSONFloat `json:"limit_7d_credits"`
	WindowMode7d     *string           `json:"window_mode_7d"`
	WeekResetDay     *int              `json:"week_reset_day"`
	WeekResetHour    *int              `json:"week_reset_hour"`
	Limit30d         optionalJSONFloat `json:"limit_30d_credits"`
	WindowMode30d    *string           `json:"window_mode_30d"`
	MFA              json.RawMessage   `json:"mfa"`
	Passkey          json.RawMessage   `json:"passkey"`
}

type userPeriodLimitResetRequest struct {
	Windows []string `json:"windows"`
	Mode    string   `json:"mode"`
}

type userJSONFieldType uint8

const userJSONFieldErrInvalidType = "invalid_type"

const (
	userJSONFieldBoolean userJSONFieldType = iota
	userJSONFieldString
	userJSONFieldNumber
	userJSONFieldInteger
	userJSONFieldStringArray
)

type userJSONFieldSpec struct {
	Field    string
	Code     string
	Expected string
	Type     userJSONFieldType
}

var userWriteJSONFieldSpecs = []userJSONFieldSpec{
	{Field: "credits_unlimited", Code: userJSONFieldErrInvalidType, Expected: "a boolean or null", Type: userJSONFieldBoolean},
	{Field: "timezone", Code: cluster.PeriodLimitErrInvalidTimezone, Expected: "a string or null", Type: userJSONFieldString},
	{Field: "limit_5h_credits", Code: cluster.PeriodLimitErrInvalidLimit, Expected: "a number or null", Type: userJSONFieldNumber},
	{Field: "window_mode_5h", Code: cluster.PeriodLimitErrInvalidWindowMode, Expected: "a string or null", Type: userJSONFieldString},
	{Field: "limit_1d_credits", Code: cluster.PeriodLimitErrInvalidLimit, Expected: "a number or null", Type: userJSONFieldNumber},
	{Field: "window_mode_1d", Code: cluster.PeriodLimitErrInvalidWindowMode, Expected: "a string or null", Type: userJSONFieldString},
	{Field: "limit_7d_credits", Code: cluster.PeriodLimitErrInvalidLimit, Expected: "a number or null", Type: userJSONFieldNumber},
	{Field: "window_mode_7d", Code: cluster.PeriodLimitErrInvalidWindowMode, Expected: "a string or null", Type: userJSONFieldString},
	{Field: "week_reset_day", Code: cluster.PeriodLimitErrInvalidWeekResetDay, Expected: "an integer or null", Type: userJSONFieldInteger},
	{Field: "week_reset_hour", Code: cluster.PeriodLimitErrInvalidWeekResetHour, Expected: "an integer or null", Type: userJSONFieldInteger},
	{Field: "limit_30d_credits", Code: cluster.PeriodLimitErrInvalidLimit, Expected: "a number or null", Type: userJSONFieldNumber},
	{Field: "window_mode_30d", Code: cluster.PeriodLimitErrInvalidWindowMode, Expected: "a string or null", Type: userJSONFieldString},
}

var userPeriodLimitResetJSONFieldSpecs = []userJSONFieldSpec{
	{Field: "windows", Code: cluster.PeriodLimitErrInvalidResetWindows, Expected: "an array of strings or null", Type: userJSONFieldStringArray},
	{Field: "mode", Code: cluster.PeriodLimitErrInvalidResetMode, Expected: "a string or null", Type: userJSONFieldString},
}

// ListUsers returns users.
func (h *Handler) ListUsers(c *gin.Context) {
	ctx, cancel := h.requestContext(c)
	defer cancel()

	records, errRecords := h.repo.ListUsers(ctx)
	if errRecords != nil {
		respondError(c, http.StatusInternalServerError, "user_load_failed", errRecords)
		return
	}
	items := make([]gin.H, 0, len(records))
	for _, record := range records {
		items = append(items, userRecordToMap(&record))
	}
	c.JSON(http.StatusOK, gin.H{"users": items})
}

// GetUser returns a user.
func (h *Handler) GetUser(c *gin.Context) {
	id, ok := userIDFromParam(c)
	if !ok {
		return
	}

	ctx, cancel := h.requestContext(c)
	defer cancel()
	record, errRecord := h.repo.GetUser(ctx, id)
	if errRecord != nil {
		respondUserRecordError(c, "user_load_failed", errRecord)
		return
	}
	c.JSON(http.StatusOK, gin.H{"user": userRecordToMap(record)})
}

// CreateUser creates a user.
func (h *Handler) CreateUser(c *gin.Context) {
	var body userWriteRequest
	if errBindJSON := c.ShouldBindBodyWithJSON(&body); errBindJSON != nil {
		respondUserJSONBindError(c, errBindJSON, userWriteJSONFieldSpecs)
		return
	}
	update, ok := userUpdateFromRequest(c, body, true)
	if !ok {
		return
	}

	ctx, cancel := h.requestContext(c)
	defer cancel()
	record, errCreate := h.repo.CreateUser(ctx, update)
	if errCreate != nil {
		if cluster.IsUserConflictError(errCreate) {
			respondError(c, http.StatusConflict, "user_exists", errCreate)
			return
		}
		if respondUserPeriodLimitValidationError(c, errCreate) {
			return
		}
		respondError(c, http.StatusInternalServerError, "user_create_failed", errCreate)
		return
	}
	c.JSON(http.StatusOK, gin.H{"user": userRecordToMap(record)})
}

// UpdateUser updates a user.
func (h *Handler) UpdateUser(c *gin.Context) {
	id, ok := userIDFromParam(c)
	if !ok {
		return
	}
	var body userWriteRequest
	if errBindJSON := c.ShouldBindBodyWithJSON(&body); errBindJSON != nil {
		respondUserJSONBindError(c, errBindJSON, userWriteJSONFieldSpecs)
		return
	}
	update, ok := userUpdateFromRequest(c, body, false)
	if !ok {
		return
	}

	ctx, cancel := h.requestContext(c)
	defer cancel()
	if update.MFA != nil && isRedactedMFAWrite(*update.MFA) {
		// Responses redact MFA secrets. A secret-less MFA object that still
		// claims to be enabled is the redacted form echoed back, so keep the
		// stored secret instead of wiping it.
		current, errCurrent := h.repo.GetUser(ctx, id)
		if errCurrent != nil {
			respondUserRecordError(c, "user_update_failed", errCurrent)
			return
		}
		if mfaContainsSecret(current.MFA) {
			update.MFA = nil
		} else {
			// Do not let the echo overwrite a TOTP binding made after this read.
			update.ExpectedMFA = &current.MFA
		}
	}
	record, errUpdate := h.repo.UpdateUser(ctx, id, update)
	if errUpdate != nil {
		if respondUserPeriodLimitValidationError(c, errUpdate) {
			return
		}
		respondUserRecordError(c, "user_update_failed", errUpdate)
		return
	}
	c.JSON(http.StatusOK, gin.H{"user": userRecordToMap(record)})
}

// DeleteUser deletes a user.
func (h *Handler) DeleteUser(c *gin.Context) {
	id, ok := userIDFromParam(c)
	if !ok {
		return
	}

	ctx, cancel := h.requestContext(c)
	defer cancel()
	if errDelete := h.repo.DeleteUser(ctx, id); errDelete != nil {
		respondUserRecordError(c, "user_delete_failed", errDelete)
		return
	}
	respondOK(c)
}

// GetUserPeriodLimits returns period-limit configuration and current usage.
func (h *Handler) GetUserPeriodLimits(c *gin.Context) {
	id, ok := userIDFromParam(c)
	if !ok {
		return
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	status, errStatus := h.repo.BuildUserPeriodLimitsStatus(ctx, id, time.Now().UTC())
	if errStatus != nil {
		respondUserRecordError(c, "user_period_limits_load_failed", errStatus)
		return
	}
	c.JSON(http.StatusOK, status)
}

// ResetUserPeriodLimits resets period-limit counters for a user.
func (h *Handler) ResetUserPeriodLimits(c *gin.Context) {
	id, ok := userIDFromParam(c)
	if !ok {
		return
	}
	var body userPeriodLimitResetRequest
	if errBindJSON := c.ShouldBindBodyWithJSON(&body); errBindJSON != nil && !errors.Is(errBindJSON, io.EOF) {
		respondUserJSONBindError(c, errBindJSON, userPeriodLimitResetJSONFieldSpecs)
		return
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	result, errReset := h.repo.ResetUserPeriodLimits(ctx, id, body.Windows, body.Mode, time.Now().UTC())
	if errReset != nil {
		if respondUserPeriodLimitValidationError(c, errReset) {
			return
		}
		respondUserRecordError(c, "user_period_limits_reset_failed", errReset)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"user_id": result.UserID,
		"reset": gin.H{
			"mode":    result.Mode,
			"windows": result.Windows,
			"at":      result.At,
		},
		"limits": result.Limits,
	})
}

func respondUserJSONBindError(c *gin.Context, err error, specs []userJSONFieldSpec) {
	if item, message, ok := userJSONTypeFieldError(c, specs); ok {
		respondErrorWithFieldErrors(c, http.StatusBadRequest, "invalid body", errors.New(message), []fieldErrorItem{item})
		return
	}
	respondError(c, http.StatusBadRequest, "invalid body", err)
}

func userJSONTypeFieldError(c *gin.Context, specs []userJSONFieldSpec) (fieldErrorItem, string, bool) {
	if c == nil {
		return fieldErrorItem{}, "", false
	}
	rawBody, okBody := c.Get(gin.BodyBytesKey)
	if !okBody {
		return fieldErrorItem{}, "", false
	}
	body, okBytes := rawBody.([]byte)
	if !okBytes || len(body) == 0 {
		return fieldErrorItem{}, "", false
	}
	fields := map[string]json.RawMessage{}
	if errUnmarshal := json.Unmarshal(body, &fields); errUnmarshal != nil {
		return fieldErrorItem{}, "", false
	}
	for _, spec := range specs {
		raw, exists := fields[spec.Field]
		if !exists || userJSONFieldTypeMatches(raw, spec.Type) {
			continue
		}
		return fieldErrorItem{Field: spec.Field, Code: spec.Code}, fmt.Sprintf("%s must be %s", spec.Field, spec.Expected), true
	}
	return fieldErrorItem{}, "", false
}

func userJSONFieldTypeMatches(raw json.RawMessage, expected userJSONFieldType) bool {
	switch expected {
	case userJSONFieldBoolean:
		var value *bool
		return json.Unmarshal(raw, &value) == nil
	case userJSONFieldString:
		var value *string
		return json.Unmarshal(raw, &value) == nil
	case userJSONFieldNumber:
		var value *float64
		return json.Unmarshal(raw, &value) == nil
	case userJSONFieldInteger:
		var value *int
		return json.Unmarshal(raw, &value) == nil
	case userJSONFieldStringArray:
		var value []string
		return json.Unmarshal(raw, &value) == nil
	default:
		return false
	}
}

func userUpdateFromRequest(c *gin.Context, body userWriteRequest, requireUsername bool) (cluster.UserUpdate, bool) {
	update := cluster.UserUpdate{}
	username := body.username()
	if username != nil {
		if strings.TrimSpace(*username) == "" {
			respondErrorWithFieldErrors(c, http.StatusBadRequest, "invalid body", errRequired("username"), []fieldErrorItem{
				{Field: "username", Code: "required"},
			})
			return update, false
		}
		update.Username = username
	} else if requireUsername {
		respondErrorWithFieldErrors(c, http.StatusBadRequest, "invalid body", errRequired("username"), []fieldErrorItem{
			{Field: "username", Code: "required"},
		})
		return update, false
	}
	password, errPassword := managementPasswordValue(body.Password)
	if errPassword != nil {
		respondError(c, http.StatusBadRequest, "invalid body", errPassword)
		return update, false
	}
	update.Password = password
	update.Credits = body.Credits
	update.CreditsUnlimited = body.CreditsUnlimited
	update.Timezone = body.Timezone
	update.Limit5hCredits = optionalFloatToUpdate(body.Limit5h)
	update.WindowMode5h = body.WindowMode5h
	update.Limit1dCredits = optionalFloatToUpdate(body.Limit1d)
	update.WindowMode1d = body.WindowMode1d
	update.Limit7dCredits = optionalFloatToUpdate(body.Limit7d)
	update.WindowMode7d = body.WindowMode7d
	update.WeekResetDay = body.WeekResetDay
	update.WeekResetHour = body.WeekResetHour
	update.Limit30dCredits = optionalFloatToUpdate(body.Limit30d)
	update.WindowMode30d = body.WindowMode30d
	if len(body.MFA) > 0 {
		mfa, errMFA := cluster.NormalizeJSONB(body.MFA)
		if errMFA != nil {
			respondError(c, http.StatusBadRequest, "invalid body", errMFA)
			return update, false
		}
		update.MFA = mfa
	}
	if len(body.Passkey) > 0 {
		passkey, errPasskey := cluster.NormalizeJSONB(body.Passkey)
		if errPasskey != nil {
			respondError(c, http.StatusBadRequest, "invalid body", errPasskey)
			return update, false
		}
		update.Passkey = passkey
	}
	return update, true
}

func optionalFloatToUpdate(value optionalJSONFloat) cluster.OptionalFloatUpdate {
	if !value.set {
		return cluster.OptionalFloatUpdate{}
	}
	if value.clear {
		return cluster.OptionalFloatUpdate{Set: true, Clear: true}
	}
	return cluster.OptionalFloatUpdate{Set: true, Value: value.value}
}

func (r userWriteRequest) username() *string {
	for _, value := range []*string{r.Username, r.UserName, r.UserNameDash} {
		if value == nil {
			continue
		}
		username := strings.TrimSpace(*value)
		return &username
	}
	return nil
}

func userIDFromParam(c *gin.Context) (uint, bool) {
	id, errID := cluster.ParseUserRecordID(c.Param("id"))
	if errID != nil {
		respondError(c, http.StatusBadRequest, "invalid id", errID)
		return 0, false
	}
	return id, true
}

func respondUserRecordError(c *gin.Context, code string, err error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		respondError(c, http.StatusNotFound, "not_found", err)
		return
	}
	if cluster.IsUserConflictError(err) {
		respondError(c, http.StatusConflict, "user_exists", err)
		return
	}
	if errors.Is(err, cluster.ErrUserMFAChanged) {
		respondError(c, http.StatusConflict, "user_mfa_changed", err)
		return
	}
	respondError(c, http.StatusInternalServerError, code, err)
}

func managementPasswordValue(password *string) (*string, error) {
	if password == nil {
		return nil, nil
	}
	value := *password
	if value == "" || isBcryptPasswordHash(value) {
		return &value, nil
	}
	hashed, errHash := bcrypt.GenerateFromPassword([]byte(value), bcrypt.DefaultCost)
	if errHash != nil {
		return nil, fmt.Errorf("password hash failed: %w", errHash)
	}
	next := string(hashed)
	return &next, nil
}

func isBcryptPasswordHash(value string) bool {
	if value == "" {
		return false
	}
	_, errCost := bcrypt.Cost([]byte(value))
	return errCost == nil
}

func isUserPeriodLimitValidationError(err error) bool {
	if err == nil {
		return false
	}
	var configErr cluster.PeriodLimitConfigError
	return errors.As(err, &configErr)
}

// respondUserPeriodLimitValidationError writes a 400 response carrying the
// stable field_errors contract for period-limit validation failures. It
// reports whether err was a validation error (and the response was written).
func respondUserPeriodLimitValidationError(c *gin.Context, err error) bool {
	if !isUserPeriodLimitValidationError(err) {
		return false
	}
	var configErr cluster.PeriodLimitConfigError
	if errors.As(err, &configErr) && configErr.Code != "" {
		respondErrorWithFieldErrors(c, http.StatusBadRequest, "invalid body", err, []fieldErrorItem{
			{Field: configErr.Field, Code: configErr.Code},
		})
		return true
	}
	respondError(c, http.StatusBadRequest, "invalid body", err)
	return true
}

func userRecordToMap(record *cluster.UserRecord) gin.H {
	if record == nil {
		return gin.H{}
	}
	timezone := strings.TrimSpace(record.Timezone)
	if timezone == "" {
		timezone = cluster.DefaultUserTimezone
	}
	windowMode5h := cluster.CanonicalPeriodWindowMode(record.WindowMode5h, false)
	windowMode1d := cluster.CanonicalPeriodWindowMode(record.WindowMode1d, true)
	windowMode7d := cluster.CanonicalPeriodWindowMode(record.WindowMode7d, true)
	windowMode30d := cluster.CanonicalPeriodWindowMode(record.WindowMode30d, true)
	weekResetDay := record.WeekResetDay
	if weekResetDay == 0 {
		weekResetDay = cluster.DefaultWeekResetDay
	}
	return gin.H{
		"id":                      record.ID,
		"username":                record.Username,
		"password_set":            record.Password != "",
		"credits":                 record.Credits,
		"credits_unlimited":       record.CreditsUnlimited,
		"timezone":                timezone,
		"limit_5h_credits":        record.Limit5hCredits,
		"window_mode_5h":          windowMode5h,
		"limit_1d_credits":        record.Limit1dCredits,
		"window_mode_1d":          windowMode1d,
		"limit_7d_credits":        record.Limit7dCredits,
		"window_mode_7d":          windowMode7d,
		"week_reset_day":          weekResetDay,
		"week_reset_hour":         record.WeekResetHour,
		"limit_30d_credits":       record.Limit30dCredits,
		"window_mode_30d":         windowMode30d,
		"period_limits_summary":   userPeriodLimitsSummary(record),
		"period_window_start_5h":  record.PeriodWindowStart5h,
		"period_window_start_1d":  record.PeriodWindowStart1d,
		"period_window_start_7d":  record.PeriodWindowStart7d,
		"period_window_start_30d": record.PeriodWindowStart30d,
		"usage_epoch_5h":          record.UsageEpoch5h,
		"usage_epoch_1d":          record.UsageEpoch1d,
		"usage_epoch_7d":          record.UsageEpoch7d,
		"usage_epoch_30d":         record.UsageEpoch30d,
		"mfa":                     redactMFA(record.MFA),
		"passkey":                 record.Passkey,
		"created_at":              record.CreatedAt,
		"updated_at":              record.UpdatedAt,
		"deleted_at":              deletedAtValue(record.DeletedAt),
	}
}

// userPeriodLimitsSummary derives a lightweight period-limit overview from the
// user record alone (no billing queries), so list views can flag configured
// and hard-blocked windows without per-user status calls.
func userPeriodLimitsSummary(record *cluster.UserRecord) gin.H {
	windows := []struct {
		id    string
		limit *float64
	}{
		{"5h", record.Limit5hCredits},
		{"1d", record.Limit1dCredits},
		{"7d", record.Limit7dCredits},
		{"30d", record.Limit30dCredits},
	}
	enabled := make([]string, 0, len(windows))
	zeroLimited := make([]string, 0, len(windows))
	for _, window := range windows {
		if window.limit == nil {
			continue
		}
		enabled = append(enabled, window.id)
		if *window.limit == 0 {
			zeroLimited = append(zeroLimited, window.id)
		}
	}
	return gin.H{
		"enabled_windows":    enabled,
		"zero_limit_windows": zeroLimited,
	}
}

// isMFASecretField reports whether an MFA JSON key holds secret material or
// replay state that must not leave Home through Management responses.
func isMFASecretField(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	if normalized == "last_used_counter" {
		return true
	}
	for _, marker := range []string{"secret", "seed", "recovery", "backup", "token", "password", "private", "key", "otpauth", "otp_auth"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

// redactMFAValue returns a copy of value without secret MFA fields.
func redactMFAValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if isMFASecretField(key) {
				continue
			}
			out[key] = redactMFAValue(item)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, redactMFAValue(item))
		}
		return out
	default:
		return value
	}
}

// redactMFA returns stored MFA settings without secrets. Values that are not
// JSON objects are dropped because their content cannot be classified.
func redactMFA(raw cluster.JSONB) cluster.JSONB {
	if len(raw) == 0 {
		return nil
	}
	var root map[string]any
	if errUnmarshal := json.Unmarshal(raw, &root); errUnmarshal != nil || root == nil {
		return nil
	}
	redacted, errMarshal := json.Marshal(redactMFAValue(root))
	if errMarshal != nil {
		return nil
	}
	return cluster.JSONB(redacted)
}

// mfaValueContainsSecret reports whether value contains any secret MFA field.
func mfaValueContainsSecret(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			if isMFASecretField(key) || mfaValueContainsSecret(item) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if mfaValueContainsSecret(item) {
				return true
			}
		}
	}
	return false
}

// mfaContainsSecret reports whether raw MFA settings hold secret material.
func mfaContainsSecret(raw cluster.JSONB) bool {
	if len(raw) == 0 {
		return false
	}
	var value any
	if errUnmarshal := json.Unmarshal(raw, &value); errUnmarshal != nil {
		return false
	}
	return mfaValueContainsSecret(value)
}

// isRedactedMFAWrite reports whether a Management write carries a redacted
// MFA object: a JSON object without secret fields that still claims MFA is
// enabled. Explicit disables (null, {}, or enabled=false) are not redacted
// writes and are applied as before.
func isRedactedMFAWrite(raw cluster.JSONB) bool {
	if len(raw) == 0 {
		return false
	}
	var root map[string]any
	if errUnmarshal := json.Unmarshal(raw, &root); errUnmarshal != nil || root == nil {
		return false
	}
	if mfaValueContainsSecret(root) {
		return false
	}
	// The nested TOTP object, when present, decides; otherwise the legacy
	// top-level flag does.
	if totp, ok := root["totp"].(map[string]any); ok {
		enabled, _ := totp["enabled"].(bool)
		return enabled
	}
	enabled, _ := root["enabled"].(bool)
	return enabled
}
