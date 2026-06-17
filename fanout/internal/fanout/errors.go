package fanout

import "errors"

// isErr is errors.Is, named for readability in status mapping.
func isErr(err, target error) bool { return errors.Is(err, target) }
