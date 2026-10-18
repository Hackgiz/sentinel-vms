package server

import (
	"os"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"sentinel-linux/internal/auth"
)

// TestMain lowers the bcrypt cost for the whole package: well over a hundred
// tests here create a user and sign in, and at the default cost those hashes
// dominated the package's run time under -race (CI timed out at 10 minutes).
func TestMain(m *testing.M) {
	auth.HashCost = bcrypt.MinCost
	os.Exit(m.Run())
}
