package automations

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"buddy/internal/tasks"
)

type applePush struct {
	origin, team, keyID, topic string
	key                        *ecdsa.PrivateKey
	token                      string
	refresh                    time.Time
}

func (s *Service) ConfigureMobilePush(stateDir string) error {
	c := s.config.MobilePush
	if c == nil {
		return nil
	}
	data, err := c.Key(stateDir)
	if err != nil {
		return err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" {
		return errors.New("apns.p8 must be an Apple push authentication key")
	}
	private, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return errors.New("couldn't read the APNs private key")
	}
	key, ok := private.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return errors.New("apns.p8 must use P-256")
	}
	origin := "https://api.push.apple.com"
	if c.Sandbox {
		origin = "https://api.sandbox.push.apple.com"
	}
	s.mobile = &applePush{origin: origin, team: c.TeamID, keyID: c.KeyID, topic: c.Topic(), key: key}
	return nil
}

func (s *Service) MobilePushTopic() string {
	if s.mobile == nil {
		return ""
	}
	return s.mobile.topic
}

func (p *applePush) authorization(now time.Time) (string, error) {
	if now.Before(p.refresh) {
		return p.token, nil
	}
	encode := func(value map[string]any) string {
		data, _ := json.Marshal(value)
		return base64.RawURLEncoding.EncodeToString(data)
	}
	jwt := encode(map[string]any{"alg": "ES256", "kid": p.keyID}) + "." +
		encode(map[string]any{"iss": p.team, "iat": now.Unix()})
	digest := sha256.Sum256([]byte(jwt))
	r, sig, err := ecdsa.Sign(rand.Reader, p.key, digest[:])
	if err != nil {
		return "", err
	}
	var signature [64]byte
	r.FillBytes(signature[:32])
	sig.FillBytes(signature[32:])
	p.token = jwt + "." + base64.RawURLEncoding.EncodeToString(signature[:])
	p.refresh = now.Add(30 * time.Minute)
	return p.token, nil
}

func mobilePushRequest(ctx context.Context, push *applePush, n tasks.MobileDelivery, now time.Time) (*http.Request, error) {
	// Keep chat text, provider content and project names inside Buddy.
	title, body, destination := "Buddy", "A watch has an update for you.", "inbox"
	switch n.Kind {
	case "approval":
		body = "A watch needs your approval."
	case "needs_input":
		body, destination = "A Bud needs your input.", "work"
	case "succeeded":
		body, destination = "A Bud finished a job.", "work"
	case "failed":
		body, destination = "A job needs your attention.", "work"
	}
	payload, _ := json.Marshal(map[string]any{
		"aps":   map[string]any{"alert": map[string]string{"title": title, "body": body}, "sound": "default"},
		"buddy": map[string]string{"destination": destination, "item": n.Item, "conversation": n.Conversation, "client": n.Client},
	})
	token, err := push.authorization(now)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, push.origin+"/3/device/"+n.Token, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apns-topic", push.topic)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")
	req.Header.Set("apns-collapse-id", n.ID)
	req.Header.Set("apns-expiration", strconv.FormatInt(n.CreatedAt.Add(24*time.Hour).Unix(), 10))
	return req, nil
}

func (s *Service) notifyMobile(ctx context.Context) {
	if s.mobile == nil || s.quiet(time.Now()) {
		if err := s.store.PruneMobileDeliveries(ctx, time.Now()); err != nil && ctx.Err() == nil {
			log.Printf("prune Buddy iPhone deliveries: %v", err)
		}
		return
	}
	for range 10 {
		now := time.Now()
		n, err := s.store.NextMobileDelivery(ctx, now)
		if errors.Is(err, sql.ErrNoRows) || ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Printf("Buddy iPhone delivery queue: %v", err)
			return
		}
		requestCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
		req, err := mobilePushRequest(requestCtx, s.mobile, n, now)
		if err != nil {
			cancel()
			log.Print("Buddy couldn't prepare an iPhone notification")
			return
		}
		res, err := s.client.Do(req)
		retry, invalid := err != nil, false
		if res != nil {
			var failure struct{ Reason string }
			if res.StatusCode != http.StatusOK {
				_ = json.NewDecoder(io.LimitReader(res.Body, 2048)).Decode(&failure)
				invalid = res.StatusCode == http.StatusGone || failure.Reason == "BadDeviceToken" || failure.Reason == "DeviceTokenNotForTopic"
				retry = res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500 || res.StatusCode == http.StatusForbidden
				if failure.Reason == "ExpiredProviderToken" {
					s.mobile.refresh = time.Time{}
				}
				if !invalid {
					log.Printf("Buddy iPhone notification returned HTTP %d", res.StatusCode)
				}
			}
			res.Body.Close()
		}
		cancel()
		if ctx.Err() != nil {
			return
		}
		if invalid {
			err = s.store.InvalidateMobileNotifications(ctx, n)
		} else {
			err = s.store.CompleteMobileDelivery(ctx, n, retry, now)
		}
		if err != nil {
			log.Printf("record Buddy iPhone delivery: %v", err)
			return
		}
	}
}
