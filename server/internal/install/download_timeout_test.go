package install

import "testing"

func TestDownloadClientHasNoWallClockTransferTimeout(t *testing.T) {
	if downloadClient.Timeout != 0 {
		t.Fatalf("downloadClient.Timeout = %v, want 0 so slow-but-progressing large downloads are not killed by a total request deadline", downloadClient.Timeout)
	}
}
