# Session download

OpenReplay can export the raw files for a recorded session as a ZIP archive.

The archive is intended for backup, offline inspection, and agent/automation workflows. It is not a video export.

## UI

Open a completed session replay and click **Download Session** in the player header.

The browser downloads a file named:

```text
openreplay-session-<sessionId>.zip
```

Live sessions do not show the download action.

## Authenticated application API

```http
GET /v2/api/{projectId}/sessions/{sessionId}/download
Authorization: Bearer <user-jwt>
```

The endpoint verifies that the requested session belongs to the project before streaming the archive.

## Public API

Use an Organization API Key with the project key:

```http
GET /v2/api/public/{projectKey}/sessions/{sessionId}/download
Authorization: Bearer <organization-api-key>
```

Example:

```bash
curl -fL \
  -H "Authorization: Bearer $OPENREPLAY_API_KEY" \
  "https://openreplay.example.com/v2/api/public/$PROJECT_KEY/sessions/$SESSION_ID/download" \
  -o "openreplay-session-$SESSION_ID.zip"
```

The API key must belong to the same tenant as the project, and the session must belong to that project.

## Archive format

The ZIP contains a manifest and the raw replay objects that exist for the session:

```text
manifest.json
raw/
  dom.mobs
  dom.mobe
  devtools.mob
  ...
```

Mobile/canvas replay objects are included when present. Object-storage implementations that support prefix listing export all safe objects below the session prefix.

Example `manifest.json`:

```json
{
  "format": "openreplay-session-export",
  "version": 1,
  "sessionId": "4020541843067130369",
  "files": [
    "raw/devtools.mob",
    "raw/dom.mobe",
    "raw/dom.mobs"
  ]
}
```

Archive paths are normalized and unsafe traversal paths are rejected.

## Responses

- `200` — ZIP stream
- `400` — invalid project/session identifier
- `401` — missing or invalid API credentials
- `404` — project/session/archive not found
- `500` — archive generation or storage failure
