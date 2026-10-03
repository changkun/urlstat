-- Copyright 2021 Changkun Ou. All rights reserved.
-- Use of this source code is governed by a MIT
-- license that can be found in the LICENSE file.

-- Where a visit came from: the site that linked to the page, a campaign tag
-- from the page's address, 'internal' for another page of the same site, or
-- the empty string when the browser named nothing. NULL means it was not
-- recorded: every visit before this column, and any since from a script that
-- does not report it. The referer column keeps the address as it was sent.
--
-- The service applies this file itself when it starts. On a large table,
-- create the index beforehand with CREATE INDEX CONCURRENTLY, so that
-- recording is not held up while it is built.
ALTER TABLE visits ADD COLUMN IF NOT EXISTS came_from TEXT;

-- Index for the dashboard's breakdown by where visits came from. It covers
-- only the visits that recorded it.
CREATE INDEX IF NOT EXISTS idx_visits_came_from
    ON visits (hostname, created_at DESC, came_from, path, ip)
    WHERE came_from IS NOT NULL;
