// capacity-ci-history-init exclusively creates an empty, private local
// history snapshot. It never loads gateway/CI config or performs network I/O.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

const usage = "usage: capacity-ci-history-init --initialize-empty-store DIRECTORY DIRECTORY_DEVICE DIRECTORY_INODE"

func positiveDecimal(raw string) (uint64, bool) {
	if len(raw) == 0 || len(raw) > 20 || raw[0] < '1' || raw[0] > '9' {
		return 0, false
	}
	n, err := strconv.ParseUint(raw, 10, 64)
	return n, err == nil && strconv.FormatUint(n, 10) == raw
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 4 || args[0] != "--initialize-empty-store" {
		_, _ = fmt.Fprintln(stderr, usage)
		return 2
	}
	device, deviceOK := positiveDecimal(args[2])
	inode, inodeOK := positiveDecimal(args[3])
	review := cihealth.HistoryDirectoryReview{Directory: args[1], Device: device, Inode: inode}
	if !deviceOK || !inodeOK || !review.Valid() {
		_, _ = fmt.Fprintln(stderr, usage)
		return 2
	}
	if err := cihealth.InitializeReviewedEmptyHistory(ctx, review); err != nil {
		_, _ = fmt.Fprintln(stderr, "history initialization not confirmed; review the existing directory before retrying")
		return 1
	}
	if _, err := fmt.Fprintln(stdout, "Empty CI history initialized. Independent startup review is still required."); err != nil {
		_, _ = fmt.Fprintln(stderr, "history was initialized, but the report could not be written; verify the existing snapshot")
		return 1
	}
	return 0
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	cancel()
	os.Exit(code)
}
