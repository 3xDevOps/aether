package protocol

// Web Push methods. Every one acts on the caller's own devices.
const (
	// MethodPushStatus returns the server's push key and whether one of the
	// caller's devices is subscribed.
	MethodPushStatus = "push.status"
	// MethodPushSubscribe stores a browser's push subscription for the caller.
	MethodPushSubscribe = "push.subscribe"
	// MethodPushUnsubscribe removes one of the caller's subscriptions.
	MethodPushUnsubscribe = "push.unsubscribe"
	// MethodPushTest sends a test notification to one of the caller's
	// subscriptions and fails with the push service's own error.
	MethodPushTest = "push.test"
	// MethodPushActive records that the caller is using a dashboard, which
	// holds their notifications until they stop.
	MethodPushActive = "push.active"
)

// PushStatusParams names the device asking; empty asks for the key alone.
type PushStatusParams struct {
	Endpoint string `json:"endpoint,omitempty"`
}

// PushStatusResult carries the VAPID public key as the unpadded base64url
// uncompressed P-256 point a browser subscribes with.
type PushStatusResult struct {
	PublicKey  string `json:"public_key"`
	Subscribed bool   `json:"subscribed"`
}

// PushSubscribeParams is a browser PushSubscription as its toJSON gives it.
type PushSubscribeParams struct {
	Endpoint string   `json:"endpoint"`
	Keys     PushKeys `json:"keys"`
}

// PushKeys are the subscription's unpadded base64url client keys.
type PushKeys struct {
	P256DH string `json:"p256dh"`
	Auth   string `json:"auth"`
}

// PushEndpointParams names one of the caller's subscriptions.
type PushEndpointParams struct {
	Endpoint string `json:"endpoint"`
}
