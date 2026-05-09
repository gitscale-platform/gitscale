package restapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/application/identity"
	"github.com/gitscale-platform/gitscale/plane/application/repositories"
)

func TestMapErr_exhaustiveSentinels(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(discard{}, nil))
	cases := []struct {
		name     string
		err      error
		wantHTTP int
		wantCode ErrorCode
	}{
		{"invalid email", identity.ErrInvalidEmail, http.StatusBadRequest, CodeValidationFailed},
		{"empty display name", identity.ErrEmptyDisplayName, http.StatusBadRequest, CodeValidationFailed},
		{"empty role", identity.ErrEmptyRole, http.StatusBadRequest, CodeValidationFailed},
		{"invalid slug", repositories.ErrInvalidSlug, http.StatusBadRequest, CodeValidationFailed},
		{"empty name", repositories.ErrEmptyName, http.StatusBadRequest, CodeValidationFailed},
		{"invalid visibility", repositories.ErrInvalidVisibility, http.StatusBadRequest, CodeValidationFailed},
		{"user not found", identity.ErrUserNotFound, http.StatusNotFound, CodeNotFound},
		{"agent not found", identity.ErrAgentNotFound, http.StatusNotFound, CodeNotFound},
		{"repo not found", repositories.ErrRepositoryNotFound, http.StatusNotFound, CodeNotFound},
		{"slug conflict", repositories.ErrSlugAlreadyExists, http.StatusConflict, CodeConflict},
		{"not implemented", identity.ErrNotImplemented, http.StatusNotImplemented, CodeInternal},
		{"deadline", context.DeadlineExceeded, http.StatusGatewayTimeout, CodeInternal},
		{"unknown", errors.New("boom"), http.StatusInternalServerError, CodeInternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotHTTP, gotCode, _ := mapErr(context.Background(), logger, tc.err)
			if gotHTTP != tc.wantHTTP {
				t.Errorf("status: got %d want %d", gotHTTP, tc.wantHTTP)
			}
			if gotCode != tc.wantCode {
				t.Errorf("code: got %s want %s", gotCode, tc.wantCode)
			}
		})
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
