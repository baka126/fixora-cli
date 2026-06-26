package image

import "testing"

func TestDockerHubTrustRanking(t *testing.T) {
	official, _ := dockerHubTrust(true, false, 0, 0)
	verified, _ := dockerHubTrust(false, true, 0, 0)
	popular, _ := dockerHubTrust(false, false, 20_000_000, 200)
	if !(official > verified && verified > popular && popular > 0) {
		t.Fatalf("unexpected trust ranking: official=%d verified=%d popular=%d", official, verified, popular)
	}
}

func TestSimilarRepository(t *testing.T) {
	if !similarRepository("vendor/stress-ng", "stress-ng") {
		t.Fatal("expected exact image leaf match")
	}
	if similarRepository("vendor/nginx", "stress-ng") {
		t.Fatal("unrelated image should not match")
	}
}
