package auth

import "testing"

func TestPasswordHashAndVerify(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if !VerifyPassword("correct horse battery staple", hash) {
		t.Fatalf("expected password to verify")
	}
	if VerifyPassword("wrong", hash) {
		t.Fatalf("wrong password must fail")
	}
}
