// Command nats-relay bridges HTTP webhooks and NATS JetStream subjects.
package main

import (
	"os"

	"github.com/wyrd-company/nats-relay/internal/app"
)

func main() {
	os.Exit(app.Main(os.Args))
}
