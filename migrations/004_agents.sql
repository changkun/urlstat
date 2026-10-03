-- Copyright 2021 Changkun Ou. All rights reserved.
-- Use of this source code is governed by a MIT
-- license that can be found in the LICENSE file.

-- What each user agent string was read as: its browser, operating system
-- and device kind ('desktop', 'mobile', 'tablet', 'bot', or '' when the
-- agent says nothing). The dashboard groups visits by these, and reading a
-- string once here is cheaper than reading it for every visit. The strings
-- are keyed by their md5, since some are longer than an index entry may be.
-- Rows are added the first time a period containing the string is looked
-- at; rules is the version of the rules that read it (agentRules in
-- agent.go). The service applies this file itself when it starts.
CREATE TABLE IF NOT EXISTS agents (
    hash TEXT PRIMARY KEY,
    ua TEXT NOT NULL,
    browser TEXT NOT NULL,
    os TEXT NOT NULL,
    device TEXT NOT NULL,
    rules INT NOT NULL
);
