-- Bootstrap for the branchbox oku-account Postgres.
--
-- Mirrors the repo's scripts/dev/postgres-init.sh: the deployed image ships
-- generate_uuidv7() and the migrations assume it already exists rather than
-- creating it, so a stock Postgres needs it defined before any migration runs.
-- Runs once, on an empty data directory.

CREATE DATABASE retail;

\connect postgres
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- A v7 UUID: a millisecond timestamp in the leading 48 bits followed by random
-- bits, with the version and variant marked. Time-ordered, so it indexes
-- better than v4.
CREATE OR REPLACE FUNCTION generate_uuidv7() RETURNS uuid AS $$
    SELECT encode(
        set_bit(
            set_bit(
                overlay(uuid_send(gen_random_uuid())
                    PLACING substring(int8send(floor(extract(epoch FROM clock_timestamp()) * 1000)::bigint) FROM 3)
                    FROM 1 FOR 6),
                52, 1),
            53, 1),
        'hex')::uuid;
$$ LANGUAGE sql VOLATILE;

-- Retail migrations name their schema explicitly (retail.<table>) and the
-- production migrator creates it first. Accounts migrations ignore it.
CREATE SCHEMA IF NOT EXISTS retail;

\connect retail
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE OR REPLACE FUNCTION generate_uuidv7() RETURNS uuid AS $$
    SELECT encode(
        set_bit(
            set_bit(
                overlay(uuid_send(gen_random_uuid())
                    PLACING substring(int8send(floor(extract(epoch FROM clock_timestamp()) * 1000)::bigint) FROM 3)
                    FROM 1 FOR 6),
                52, 1),
            53, 1),
        'hex')::uuid;
$$ LANGUAGE sql VOLATILE;

CREATE SCHEMA IF NOT EXISTS retail;
