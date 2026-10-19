// Package webpush manages device subscriptions and a durable notification outbox.
package webpush

import (
	"crypto/ecdh"
	"encoding/base64"
	"errors"
	"net/url"
	"os"
	"strings"

	push "github.com/SherClockHolmes/webpush-go"
)

type Config struct {
	PublicKey, PrivateKey, Subject string
	Enabled                        bool
}

func FromEnv() (Config, error) {
	c := Config{Enabled: os.Getenv("JETAIME_WEB_PUSH_ENABLED") == "true", PublicKey: os.Getenv("JETAIME_WEB_PUSH_PUBLIC_KEY"), PrivateKey: os.Getenv("JETAIME_WEB_PUSH_PRIVATE_KEY"), Subject: os.Getenv("JETAIME_WEB_PUSH_SUBJECT")}
	if !c.Enabled {
		return c, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.PrivateKey)
	if err != nil {
		return c, errors.New("invalid Web Push private key")
	}
	key, err := ecdh.P256().NewPrivateKey(raw)
	if err != nil || base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()) != c.PublicKey {
		return c, errors.New("Web Push key pair does not match")
	}
	subject, err := url.Parse(c.Subject)
	if err != nil || !((subject.Scheme == "mailto" && subject.Opaque != "") || (subject.Scheme == "https" && subject.Hostname() != "" && subject.User == nil)) {
		return c, errors.New("Web Push subject must be a contact mailto or HTTPS URL")
	}
	return c, nil
}

// Only known browser push services may receive outbound requests. Never follow redirects.
func validSubscription(s push.Subscription) bool {
	u, err := url.Parse(s.Endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") || len(s.Endpoint) > 2048 {
		return false
	}
	host := strings.ToLower(u.Hostname())
	allowed := strings.HasSuffix(host, ".push.apple.com") || host == "fcm.googleapis.com" || host == "fcm.push.google.com" || host == "updates.push.services.mozilla.com"
	public, e1 := base64.RawURLEncoding.DecodeString(s.Keys.P256dh)
	auth, e2 := base64.RawURLEncoding.DecodeString(s.Keys.Auth)
	if !allowed || e1 != nil || e2 != nil || len(auth) != 16 {
		return false
	}
	_, err = ecdh.P256().NewPublicKey(public)
	return err == nil
}
