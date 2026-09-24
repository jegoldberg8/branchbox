-- Bootstrap for the branchbox oku-account ClickHouse.
--
-- Three databases, one per ClickHouse consumer in the repo, so none of them
-- can silently write into another's tables:
--
--   retail          retail analytics
--   okubase         user tracking events
--   account_candles OHLCV candles (ClickHouse-only, so the API and workers
--                   refuse to start without it)
--
-- Only the databases are created here. The schemas themselves belong to the
-- application and are applied from inside a branch container with the same
-- commands deployments use (`go run ./kocmd obcli migrate` and
-- `go run ./kocmd candlemigrate`), so the schema follows the branch's code
-- rather than a copy frozen in this file.

CREATE DATABASE IF NOT EXISTS retail;
CREATE DATABASE IF NOT EXISTS okubase;
CREATE DATABASE IF NOT EXISTS account_candles;
