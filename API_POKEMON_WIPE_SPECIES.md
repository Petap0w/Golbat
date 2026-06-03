# Golbat API — Wipe Pokémon Species from Cache

Reference for an automated agent (LLM/tool) that needs to evict every cached
Pokémon of a single species from a running Golbat instance.

## Summary

| | |
|---|---|
| **Method** | `POST` |
| **Path** | `/api/pokemon/species/{pokedex_id}/wipe` |
| **Auth** | `X-Golbat-Secret: <api_secret>` header (only if the server has `api_secret` configured) |
| **Request body** | none |
| **Success status** | `200 OK` |
| **Effect** | Removes all in-memory cached Pokémon whose species equals `pokedex_id`. Database rows are **not** modified. |

## Path parameter

| Name | Type | Constraints | Meaning |
|---|---|---|---|
| `pokedex_id` | integer | `0` … `32767` (fits a signed 16-bit int) | The Pokédex species number to wipe (e.g. `25` = Pikachu, `132` = Ditto). |

> **Important:** `pokedex_id` is the **species** number (a Pokédex ID), **not**
> an encounter ID. Elsewhere in this API a path segment named `pokemon_id`
> (e.g. `GET /api/pokemon/id/{pokemon_id}`) actually refers to a per-spawn
> **encounter ID** (a `uint64`). This endpoint deliberately uses `pokedex_id`
> to avoid that confusion. Do not pass an encounter ID here.

## Authentication

If the Golbat server is configured with an `api_secret`, every `/api/*` request
must include the header:

```
X-Golbat-Secret: <api_secret>
```

A missing or wrong secret returns `401 Unauthorized` with the body `Unauthorised`.
If the server has no `api_secret` set, the header is not required.

## Request

No request body. No query parameters. The species is taken entirely from the URL.

### curl

```bash
# Wipe all cached Pikachu (species 25)
curl -X POST \
  -H "X-Golbat-Secret: $GOLBAT_SECRET" \
  "http://<host>:<port>/api/pokemon/species/25/wipe"
```

## Response

### 200 OK

JSON object reporting what was removed:

```json
{
  "pokedex_id": 25,
  "removed": 1432
}
```

| Field | Type | Meaning |
|---|---|---|
| `pokedex_id` | integer | Echo of the species that was wiped. |
| `removed` | integer | Count of cache entries actually evicted. `0` is normal and means none were cached. |

### 400 Bad Request

Returned (empty body) when `pokedex_id` is not an integer, is negative, or
exceeds `32767`.

### 401 Unauthorized

Returned (body `Unauthorised`) when `api_secret` is configured and the
`X-Golbat-Secret` header is absent or incorrect.

## Behavioural notes (read before automating)

1. **Cache only — not the database.** This evicts entries from Golbat's
   in-memory Pokémon cache (and its spatial R-tree / lookup indexes). Rows
   already persisted to MySQL/MariaDB are left untouched. After a wipe, the
   species disappears from `/api/pokemon/scan*`, `/api/pokemon/notable`,
   `/api/pokemon/search`, and `/api/pokemon/id/{encounter_id}` results because
   those read paths are served from the cache.

2. **Point-in-time, not a permanent ban.** The wipe removes what is cached at
   the moment of the call. New scan data arriving afterwards (or a save that was
   already in flight) can legitimately re-introduce that species. To keep a
   species cleared you must call the endpoint again after new data arrives — it
   is safe and cheap to repeat.

3. **Idempotent-ish.** Calling it twice in a row typically returns a large
   `removed` count on the first call and a small or `0` count on the second.
   Re-calling never errors.

4. **Per-species only.** There is no bulk/multi-species form. To wipe several
   species, issue one request per species (requests are independent and may be
   sent concurrently).

5. **Cost.** The handler scans the entire Pokémon lookup index once per call
   (O(total cached Pokémon)). It is lock-free for the scan phase and safe to run
   while the server is under normal scan/ingest load, but avoid issuing it in a
   tight high-frequency loop.

6. **Concurrency safety.** The operation is safe to run alongside all other API
   reads/writes; it locks each entity individually before evicting it and never
   holds more than one entity lock at a time.

## Decision guide for an agent

- Want to **clear a species from live map/API results right now** → call this endpoint.
- Want it **gone from the database** → this endpoint is not sufficient; a DB
  operation is required (out of scope here).
- Want to **look up a single Pokémon by its encounter id** → use
  `GET /api/pokemon/id/{encounter_id}` instead; this endpoint will reject or
  misinterpret an encounter id.

## Examples

```bash
# Ditto (132)
curl -X POST -H "X-Golbat-Secret: $GOLBAT_SECRET" \
  "http://localhost:9001/api/pokemon/species/132/wipe"
# -> {"pokedex_id":132,"removed":7}

# No instances cached
curl -X POST -H "X-Golbat-Secret: $GOLBAT_SECRET" \
  "http://localhost:9001/api/pokemon/species/9999/wipe"
# -> {"pokedex_id":9999,"removed":0}

# Invalid (out of int16 range)
curl -X POST -H "X-Golbat-Secret: $GOLBAT_SECRET" \
  "http://localhost:9001/api/pokemon/species/40000/wipe"
# -> HTTP 400, empty body
```
