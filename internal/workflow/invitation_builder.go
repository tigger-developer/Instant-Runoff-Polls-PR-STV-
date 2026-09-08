// ABOUTME: Builds invitation messages from recoverable persisted grant claims.
// ABOUTME: It derives absolute links only from configured public URL and active key.
package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/store"
)

func NewInvitationMessageBuilder(repository *store.Store, baseURL, keyID string, key []byte, randomness io.Reader, now func() time.Time) DeliveryMessageBuilder {
	return func(ctx context.Context, item *store.ClaimedWork, attempt store.DeliveryAttempt) (Message, error) {
		if ctx == nil || repository == nil || strings.TrimSpace(baseURL) == "" || keyID == "" || len(key) != 32 || randomness == nil || now == nil || item == nil || item.ID == "" || item.ClaimToken == "" || attempt.MessageKind != "invitation" {
			return Message{}, errors.New("invitation builder dependencies are invalid")
		}
		at := now()
		claims := GrantClaims{Version: 1, KeyID: keyID, Purpose: ParticipantGrant, PrincipalID: attempt.ParticipantID, PollID: attempt.PollID, ContactID: attempt.ContactID, IssuedAt: at, ExpiresAt: at.Add(24 * time.Hour)}
		token, payload, err := IssuePersistableGrant(claims, key, randomness)
		if err != nil {
			return Message{}, err
		}
		tokenHash := sha256.Sum256([]byte(token))
		material := store.GrantMaterial{ID: hex.EncodeToString(tokenHash[:]), KeyID: keyID, TokenHash: tokenHash[:], ClaimsJSON: payload, IssuedAt: claims.IssuedAt, ExpiresAt: claims.ExpiresAt}
		persistedPayload, err := repository.PrepareDeliveryGrant(ctx, item.ID, item.ClaimToken, keyID, at, material)
		if err != nil {
			return Message{}, err
		}
		token, err = RestoreGrant(persistedPayload, key)
		if err != nil {
			return Message{}, err
		}
		link := strings.TrimRight(baseURL, "/") + "/auth/verify?grant=" + url.QueryEscape(token)
		return InvitationMessage(attempt.RecipientEmail, attempt.Question, link)
	}
}
