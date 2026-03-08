package clickhouse_connect

import (
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"fmt"
	"net/url"
	"os"

	_ "github.com/ClickHouse/clickhouse-go/v2"
)

type Params struct {
	User       string
	Password   string
	Host       string
	Port       string
	Database   string
	Secure     bool
	SkipVerify bool
	SSLCA      string
	SSLCert    string
	SSLKey     string
}

func GetConnAndHost(params Params) (*sql.DB, string, error) {
	var host string

	if params.Port != "" {
		host = fmt.Sprintf("%s:%s", params.Host, params.Port)
	} else {
		host = fmt.Sprintf("%s:9000", params.Host)
	}

	opts := make(url.Values)

	if params.Database != "" {
		opts.Set("database", params.Database)
	}
	if params.User != "" {
		opts.Set("username", params.User)
	}
	if params.Password != "" {
		opts.Set("password", params.Password)
	}

	var tlsConfig *tls.Config
	if params.SSLCA != "" || params.SSLCert != "" || params.SSLKey != "" {
		tlsConfig = &tls.Config{
			InsecureSkipVerify: params.SkipVerify,
		}

		if params.SSLCA != "" {
			caCertPool := x509.NewCertPool()
			caCert, err := os.ReadFile(params.SSLCA)
			if err != nil {
				return nil, "", fmt.Errorf("failed to read CA cert: %w", err)
			}
			if ok := caCertPool.AppendCertsFromPEM(caCert); !ok {
				return nil, "", fmt.Errorf("failed to append CA cert")
			}
			tlsConfig.RootCAs = caCertPool
		}

		if params.SSLCert != "" && params.SSLKey != "" {
			cert, err := tls.LoadX509KeyPair(params.SSLCert, params.SSLKey)
			if err != nil {
				return nil, "", fmt.Errorf("failed to load client cert: %w", err)
			}
			tlsConfig.Certificates = []tls.Certificate{cert}
		}

		opts.Set("secure", "true")
	} else if params.Secure {
		opts.Set("secure", "true")
	}

	var dsn string
	if len(opts) > 0 {
		dsn = fmt.Sprintf("clickhouse://%s?%s", host, opts.Encode())
	} else {
		dsn = fmt.Sprintf("clickhouse://%s", host)
	}

	conn, err := sql.Open("clickhouse", dsn)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create connection: %w", err)
	}

	if err = conn.Ping(); err != nil {
		return nil, "", fmt.Errorf("failed to ping ClickHouse: %w", err)
	}

	return conn, host, nil
}