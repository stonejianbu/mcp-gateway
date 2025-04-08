package xlog

import (
	"github.com/labstack/echo/v4"
	"github.com/labstack/gommon/log"
)

type Logger interface {
	echo.Logger
}

type logger struct {
	echo.Logger
}

func NewLogger(name string) Logger {
	lg := log.New(name)
	lg.SetLevel(log.Lvl(0))
	return &logger{
		Logger: lg,
	}
}

func WithEchoLogger(lg echo.Logger) Logger {
	lg.SetLevel(0)
	return &logger{
		Logger: lg,
	}
}
