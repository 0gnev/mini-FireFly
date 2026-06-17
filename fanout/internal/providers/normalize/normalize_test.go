package normalize

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestToEUR(t *testing.T) {
	fx := DefaultFX()
	cases := []struct {
		amount   string
		currency string
		want     string
		wantErr  bool
	}{
		{"142.5", "EUR", "142.50", false},
		{"158.90", "USD", "146.19", false},   // ×0.92 = 146.188 -> 146.19
		{"16690.00", "RSD", "141.87", false}, // ×0.0085 = 141.865 -> 141.87
		{"100", "GBP", "", true},             // unknown currency
	}
	for _, c := range cases {
		amt := decimal.RequireFromString(c.amount)
		got, err := fx.ToEUR(amt, c.currency)
		if c.wantErr {
			if err == nil {
				t.Errorf("ToEUR(%s,%s) want err", c.amount, c.currency)
			}
			continue
		}
		if err != nil {
			t.Errorf("ToEUR(%s,%s) err: %v", c.amount, c.currency, err)
			continue
		}
		if got != c.want {
			t.Errorf("ToEUR(%s,%s) = %q want %q", c.amount, c.currency, got, c.want)
		}
	}
}

func TestFXFromEnv(t *testing.T) {
	fx, err := FXFromEnv("0.90", "0.01")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	got, _ := fx.ToEUR(decimal.RequireFromString("100"), "USD")
	if got != "90.00" {
		t.Errorf("USD override = %q want 90.00", got)
	}
	got, _ = fx.ToEUR(decimal.RequireFromString("100"), "RSD")
	if got != "1.00" {
		t.Errorf("RSD override = %q want 1.00", got)
	}
	// Empty falls back to defaults.
	fx2, _ := FXFromEnv("", "")
	def := DefaultFX()
	if !fx2.USDToEUR.Equal(def.USDToEUR) || !fx2.RSDToEUR.Equal(def.RSDToEUR) {
		t.Errorf("empty env should yield defaults")
	}
	// Invalid value errors.
	if _, err := FXFromEnv("abc", ""); err == nil {
		t.Errorf("invalid FX_USD_EUR should error")
	}
}

func TestOfferID(t *testing.T) {
	id := OfferID("a", "A-981")
	if len(id) != len("a:")+8 {
		t.Errorf("offer id %q wrong length", id)
	}
	if id[:2] != "a:" {
		t.Errorf("offer id %q missing provider prefix", id)
	}
	// Deterministic.
	if OfferID("a", "A-981") != id {
		t.Errorf("offer id not deterministic")
	}
}

func TestCentsToDecimal(t *testing.T) {
	if got := CentsToDecimal(15890).String(); got != "158.9" {
		t.Errorf("CentsToDecimal(15890) = %q want 158.9", got)
	}
}

func TestLocationKnownAndUnknown(t *testing.T) {
	if _, ok := Location("BEG"); !ok {
		t.Errorf("BEG should be known")
	}
	if _, ok := Location("ZZZ"); ok {
		t.Errorf("ZZZ should be unknown")
	}
}
