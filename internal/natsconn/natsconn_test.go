package natsconn

import (
	"testing"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
)

func TestCredsOptionParsesCredentialsFileContents(t *testing.T) {
	account, _ := nkeys.CreateAccount()
	user, _ := nkeys.CreateUser()
	pub, _ := user.PublicKey()
	token, err := jwt.NewUserClaims(pub).Encode(account)
	if err != nil {
		t.Fatal(err)
	}
	seed, _ := user.Seed()
	creds, err := jwt.FormatUserConfig(token, seed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := credsOption(string(creds)); err != nil {
		t.Fatal(err)
	}
	if _, err := credsOption("not a credentials file"); err == nil {
		t.Fatal("expected an error for malformed credentials")
	}
}
