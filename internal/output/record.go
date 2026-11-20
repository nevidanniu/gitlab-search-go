package output

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"

	"gitlab-search-go/internal/search"
)

// Record writes a whole run to one destination: the resolved parameters, the matches and
// the closing report. Whatever the console shows is also what a results file receives.
type Record struct {
	bw     *bufio.Writer
	format string
}

func NewRecord(w io.Writer, format string) *Record {
	return &Record{bw: bufio.NewWriter(w), format: format}
}

func (r *Record) Parameters(p Parameters) error {
	if r.format == "json" {
		raw, err := json.Marshal(p.asJSON())
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(r.bw, "{\"parameters\":%s,\"results\":", raw); err != nil {
			return err
		}
	} else {
		WriteParameters(r.bw, p)
	}
	return r.bw.Flush()
}

// Matches streams the spooled hits, the only part that can be large.
func (r *Record) Matches(results []search.Spooled, term string) error {
	var err error
	if r.format == "json" {
		err = writeResultsJSON(r.bw, results)
	} else {
		err = renderPlain(r.bw, results, term)
	}
	if err != nil {
		return err
	}
	return r.bw.Flush()
}

func (r *Record) Report(rep Report) error {
	if r.format == "json" {
		raw, err := json.Marshal(rep.asJSON())
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(r.bw, ",\"report\":%s}\n", raw)
		if err != nil {
			return err
		}
	} else {
		fmt.Fprintln(r.bw)
		WriteReport(r.bw, rep)
	}
	return r.bw.Flush()
}
