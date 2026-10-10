package log

import (
	"fmt"
	"time"

	"github.com/sirupsen/logrus"
)

// CustomFormatter 继承 logrus.TextFormatter，追加格式化
type CustomFormatter struct {
	logrus.TextFormatter
	StartTime time.Time
}

func (f *CustomFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	elapsed := int(time.Since(f.StartTime).Seconds())
	t := entry.Time.Format("2006-01-02 15:04:05")
	line := fmt.Sprintf("%s[%d] %s %s\n", entry.Level, elapsed, t, entry.Message)
	return []byte(line), nil
}
