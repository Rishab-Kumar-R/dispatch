package store

import (
	"errors"
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	Host, Port, Name string
	SSLMode          string
	SecretARN        string
	MaxConns         int32
	AppName          string
}

func ConfigFromEnv(appName string) (Config, error) {
	c := Config{
		Host:      os.Getenv("DB_HOST"),
		Port:      os.Getenv("DB_PORT"),
		Name:      os.Getenv("DB_NAME"),
		SSLMode:   os.Getenv("DB_SSLMODE"),
		SecretARN: os.Getenv("DB_SECRET_ARN"),
		MaxConns:  2,
		AppName:   appName,
	}
	if c.SSLMode == "" {
		c.SSLMode = "require"
	}
	if v := os.Getenv("DB_MAX_CONNS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 20 {
			return Config{}, fmt.Errorf("DB_MAX_CONNS must be 1-20, got %q", v)
		}

		c.MaxConns = int32(n)
	}
	if c.Host == "" || c.Port == "" || c.Name == "" || c.SecretARN == "" {
		return Config{}, errors.New("missing env: DB_HOST, DB_PORT, DB_NAME, DB_SECRET_ARN")
	}
	
	return c, nil
}
