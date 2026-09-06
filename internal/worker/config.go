package worker

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

type Config struct {
	DatabaseURL string
	WorkerID    string
	Poll        time.Duration
}

// LoadConfig consumes trusted worker settings. The CLI loads
// .env before calling this function; explicit environment values take precedence.
func LoadConfig(getenv func(string) string) (Config, error) {
	config := Config{
		DatabaseURL: getenv("DATABASE_URL"),
		WorkerID:    getenv("CIRCULAR_WORKER_ID"),
		Poll:        time.Second,
	}
	if config.DatabaseURL == "" {
		config.DatabaseURL = "postgresql://circular:circular@localhost:5432/circular"
	}
	if config.WorkerID == "" {
		host, err := os.Hostname()
		if err != nil {
			return Config{}, fmt.Errorf("determine worker hostname")
		}
		config.WorkerID = host + ":" + uuid.NewString()
	}
	if err := ValidateID(config.WorkerID); err != nil {
		return Config{}, err
	}
	if selected := getenv("CIRCULAR_GO_EXECUTOR"); selected != "" && selected != "go" {
		return Config{}, fmt.Errorf("CIRCULAR_GO_EXECUTOR is retired; remove it to use native Go execution")
	}
	if value := getenv("CIRCULAR_POLL_INTERVAL_SECONDS"); value != "" {
		seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) ||
			seconds <= 0 || seconds >= float64(math.MaxInt64)/float64(time.Second) {
			return Config{}, fmt.Errorf("CIRCULAR_POLL_INTERVAL_SECONDS must be a finite positive duration")
		}
		config.Poll = time.Duration(seconds * float64(time.Second))
		if config.Poll <= 0 {
			return Config{}, fmt.Errorf("CIRCULAR_POLL_INTERVAL_SECONDS must be at least one nanosecond")
		}
	}
	return config, nil
}

func ValidateID(value string) error {
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 200 || strings.ContainsRune(value, 0) {
		return fmt.Errorf("worker ID must contain 1 through 200 valid, non-NUL Unicode characters")
	}
	return nil
}
