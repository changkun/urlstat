-- Copyright 2021 Changkun Ou. All rights reserved.
-- Use of this source code is governed by a MIT
-- license that can be found in the LICENSE file.

CREATE TABLE visits (
    id BIGSERIAL PRIMARY KEY,
    hostname VARCHAR(255) NOT NULL,
    visitor_id UUID NOT NULL,
    path TEXT NOT NULL,
    ip INET NOT NULL,
    ua TEXT,
    referer TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Index for dashboard aggregation (hostname + time filter + GROUP BY path, ip)
CREATE INDEX idx_visits_hostname_time_path_ip
    ON visits (hostname, created_at DESC, path, ip);

-- Index for page-level PV/UV counts
CREATE INDEX idx_visits_hostname_path_ip
    ON visits (hostname, path, ip);

-- Index for site-level UV queries
CREATE INDEX idx_visits_hostname_ip
    ON visits (hostname, ip);
