-- Copyright 2021 Changkun Ou. All rights reserved.
-- Use of this source code is governed by a MIT
-- license that can be found in the LICENSE file.

-- The sites and GitHub accounts whose visits are counted. A site is known by
-- its host, with its port if it has one. allowed.yml fills this table once,
-- when it is empty; after that the dashboard manages it. The service applies
-- this file itself when it starts.
CREATE TABLE IF NOT EXISTS sources (
    kind TEXT NOT NULL CHECK (kind IN ('site', 'github')),
    value TEXT NOT NULL,
    added_by TEXT NOT NULL DEFAULT '',
    added_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (kind, value)
);
