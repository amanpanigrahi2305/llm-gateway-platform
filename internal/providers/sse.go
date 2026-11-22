package providers

import (
	"bufio"
	"errors"
	"io"
	"strings"
)

// errStopSSE tells readSSE to stop early without an error (for example at [DONE]).
var errStopSSE = errors.New("stop reading stream")

// readSSE reads a Server-Sent Events stream and calls fn(event, data) once per
// event. An event ends at a blank line; several data lines are joined with "\n".
// Lines starting with ":" are comments (often keep-alives) and are skipped.
func readSSE(r io.Reader, fn func(event, data string) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024) // allow long lines
	var event string
	var data []string
	dispatch := func() error {
		if len(data) == 0 {
			event = ""
			return nil
		}
		err := fn(event, strings.Join(data, "\n"))
		event, data = "", data[:0]
		return err
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if err := dispatch(); err != nil {
				return stopIsOK(err)
			}
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return stopIsOK(dispatch())
}

func stopIsOK(err error) error {
	if errors.Is(err, errStopSSE) {
		return nil
	}
	return err
}
