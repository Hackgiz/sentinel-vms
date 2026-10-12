// Package mqtt publishes Sentinel's cameras, detections and alarms to an MQTT
// broker, with Home Assistant MQTT discovery so cameras appear in Home
// Assistant as devices without any YAML.
//
// Everything the server pipeline calls (Touch, Event, Alarm, …) only appends
// to a bounded in-memory queue and returns; one worker goroutine per broker
// connection does the network I/O. A slow or unreachable broker therefore
// never delays detection or alarm handling — when the queue is full the
// oldest message is dropped (and logged).
package mqtt

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

// Config is the broker connection and topic layout. Password is never
// serialized; the server stores it sealed, separately.
type Config struct {
	Enabled         bool   `json:"enabled"`
	Host            string `json:"host"`
	Port            int    `json:"port"`
	TLS             bool   `json:"tls"`
	Username        string `json:"username"`
	Password        string `json:"-"`
	TopicPrefix     string `json:"topicPrefix"`
	DiscoveryPrefix string `json:"discoveryPrefix"`
	// AllowCommands subscribes to the recording/detection switch topics.
	// Off by default: anyone who can publish to the broker could otherwise
	// stop recording.
	AllowCommands bool   `json:"allowCommands"`
	ClientID      string `json:"-"`
}

const (
	DefaultTopicPrefix     = "sentinel"
	DefaultDiscoveryPrefix = "homeassistant"
	DefaultPort            = 1883
	DefaultTLSPort         = 8883
)

// WithDefaults fills empty prefixes and port.
func (c Config) WithDefaults() Config {
	c.Host = strings.TrimSpace(c.Host)
	c.TopicPrefix = strings.Trim(strings.TrimSpace(c.TopicPrefix), "/")
	c.DiscoveryPrefix = strings.Trim(strings.TrimSpace(c.DiscoveryPrefix), "/")
	if c.TopicPrefix == "" {
		c.TopicPrefix = DefaultTopicPrefix
	}
	if c.DiscoveryPrefix == "" {
		c.DiscoveryPrefix = DefaultDiscoveryPrefix
	}
	if c.Port == 0 {
		c.Port = DefaultPort
		if c.TLS {
			c.Port = DefaultTLSPort
		}
	}
	if c.ClientID == "" {
		c.ClientID = "sentinel"
	}
	return c
}

// Validate checks a config that is about to be saved or tested.
func (c Config) Validate() error {
	if c.Enabled || c.Host != "" {
		if c.Host == "" {
			return errors.New("enter the broker's host name or IP address")
		}
		if strings.ContainsAny(c.Host, " /\\@?#") || strings.Contains(c.Host, "://") {
			return errors.New("broker host must be a plain host name or IP address (no tcp:// or path)")
		}
		if len(c.Host) > 253 {
			return errors.New("broker host is too long")
		}
	}
	if c.Port < 1 || c.Port > 65535 {
		return errors.New("broker port must be between 1 and 65535")
	}
	if len(c.Username) > 256 || len(c.Password) > 1024 {
		return errors.New("username or password is too long")
	}
	for _, p := range []struct{ name, v string }{{"topic prefix", c.TopicPrefix}, {"discovery prefix", c.DiscoveryPrefix}} {
		if err := validPrefix(p.v); err != nil {
			return fmt.Errorf("%s: %w", p.name, err)
		}
	}
	if c.TopicPrefix == c.DiscoveryPrefix {
		return errors.New("topic prefix and discovery prefix must differ")
	}
	return nil
}

func validPrefix(p string) error {
	if p == "" || len(p) > 64 {
		return errors.New("must be 1–64 characters")
	}
	for _, r := range p {
		ok := r == '_' || r == '-' || r == '/' || r == '.' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			return errors.New("use only letters, digits, - _ . and /")
		}
	}
	if strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") || strings.Contains(p, "//") {
		return errors.New("must not start or end with / or contain //")
	}
	return nil
}

// brokerURL is what paho dials: tcp://host:port or tls://host:port.
func (c Config) brokerURL() string {
	scheme := "tcp"
	if c.TLS {
		scheme = "tls"
	}
	return scheme + "://" + net.JoinHostPort(c.Host, fmt.Sprint(c.Port))
}

// same reports whether two configs would produce the same connection.
func (c Config) same(o Config) bool { return c == o }

// MARK: - Topics

func (c Config) topic(parts ...string) string {
	return c.TopicPrefix + "/" + strings.Join(parts, "/")
}

// AvailabilityTopic carries online/offline (retained; offline is the LWT).
func (c Config) AvailabilityTopic() string { return c.topic("available") }

// EventsTopic receives one JSON message per detection event (not retained).
func (c Config) EventsTopic() string { return c.topic("events") }

// AlarmsTopic receives one JSON message per alarm change (not retained).
func (c Config) AlarmsTopic() string { return c.topic("alarms") }

// CameraTopic is <prefix>/<camera id>/<what>.
func (c Config) CameraTopic(cameraID string, what ...string) string {
	return c.topic(append([]string{cameraID}, what...)...)
}

// nodeID is the Home Assistant discovery node for a camera.
func nodeID(cameraID string) string {
	return "sentinel_" + strings.ToLower(strings.ReplaceAll(cameraID, "-", ""))
}

func (c Config) discoveryTopic(component, cameraID, object string) string {
	return c.DiscoveryPrefix + "/" + component + "/" + nodeID(cameraID) + "/" + object + "/config"
}

// validCameraID keeps topic segments free of wildcards and separators.
func validCameraID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	return !strings.ContainsAny(id, "/+#\x00 ")
}
