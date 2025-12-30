// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package storageext

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseNacosServerAddr(t *testing.T) {
	tests := []struct {
		name       string
		addr       string
		wantCount  int
		wantErr    bool
		wantHost   string
		wantPort   uint64
	}{
		{
			name:      "single address",
			addr:      "nacos:8848",
			wantCount: 1,
			wantErr:   false,
			wantHost:  "nacos",
			wantPort:  8848,
		},
		{
			name:      "multiple addresses",
			addr:      "nacos1:8848,nacos2:8848,nacos3:8848",
			wantCount: 3,
			wantErr:   false,
		},
		{
			name:      "with spaces",
			addr:      "nacos1:8848, nacos2:8848 , nacos3:8848",
			wantCount: 3,
			wantErr:   false,
		},
		{
			name:      "localhost",
			addr:      "localhost:8848",
			wantCount: 1,
			wantErr:   false,
			wantHost:  "localhost",
			wantPort:  8848,
		},
		{
			name:      "ip address",
			addr:      "192.168.1.100:8848",
			wantCount: 1,
			wantErr:   false,
			wantHost:  "192.168.1.100",
			wantPort:  8848,
		},
		{
			name:    "invalid format - no port",
			addr:    "nacos",
			wantErr: true,
		},
		{
			name:    "invalid format - invalid port",
			addr:    "nacos:invalid",
			wantErr: true,
		},
		{
			name:    "empty address",
			addr:    "",
			wantErr: true,
		},
		{
			name:    "only spaces",
			addr:    "   ",
			wantErr: true,
		},
		{
			name:      "empty parts in list",
			addr:      "nacos1:8848,,nacos2:8848",
			wantCount: 2,
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configs, err := parseNacosServerAddr(tt.addr)
			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Len(t, configs, tt.wantCount)

			if tt.wantHost != "" && len(configs) > 0 {
				assert.Equal(t, tt.wantHost, configs[0].IpAddr)
				assert.Equal(t, tt.wantPort, configs[0].Port)
			}
		})
	}
}

func TestParseNacosServerAddr_MultipleAddresses(t *testing.T) {
	addr := "nacos1:8848,nacos2:8849,nacos3:8850"
	configs, err := parseNacosServerAddr(addr)

	require.NoError(t, err)
	require.Len(t, configs, 3)

	assert.Equal(t, "nacos1", configs[0].IpAddr)
	assert.Equal(t, uint64(8848), configs[0].Port)

	assert.Equal(t, "nacos2", configs[1].IpAddr)
	assert.Equal(t, uint64(8849), configs[1].Port)

	assert.Equal(t, "nacos3", configs[2].IpAddr)
	assert.Equal(t, uint64(8850), configs[2].Port)
}
