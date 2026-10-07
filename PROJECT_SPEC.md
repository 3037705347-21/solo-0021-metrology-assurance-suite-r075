# Metrology Assurance Suite

## Project Goal

Metrology Assurance Suite is a local HTTP service that keeps a traceable record
of laboratory measurement assurance work. It registers devices, records device
room relocations, opens a verification case for one device, records technician
measurements (each stamped with the room the device was in at the time),
requests a repeat measurement when tolerance is exceeded, and seals the final
evidence record after review.

The service is intentionally self-contained. It uses an in-memory repository
with atomic copy-on-write mutations and does not require a database or an
external service.

## Users

- Laboratory steward: registers devices and opens verification cases.
- Measurement technician: records observed values for an open case.
- Quality verifier: reviews accepted measurements and seals the case.

## Core Entities

- Device: a laboratory asset with a unique asset tag, model, room, steward, and
  lifecycle state.
- VerificationCase: one bounded assurance activity for a device, with a nominal
  value, tolerance, current state, version, and assigned steward.
- Measurement: an observed value, computed deviation, acceptance decision,
  technician, and timestamp.
- EvidenceSeal: the immutable closing record for a reviewed case, including the
  selected accepted measurement.
- AuditEvent: an append-only trace of meaningful case and device changes.

## Workflows

### 1. Register Device

The steward sends `POST /devices` with an asset tag, model, room, and steward.
The service validates the request, normalizes the asset tag, rejects duplicate
tags, and creates an active device.

### 2. Relocate Device

The steward sends `POST /devices/{id}/deployments` with a target room, actor,
and reason. The service verifies that the device exists and is active, that the
room, actor, and reason are all present, and that the target room differs from
the current room. A valid request updates the device's current room, appends a
deployment record, and records a `device_relocated` audit event in one atomic
commit. A rejected request changes nothing: the device room, deployment
history, and audit trail are untouched, so the corrected request can be
resubmitted without a state conflict.

### 3. Open Verification Case

The steward sends `POST /cases` with a device ID, nominal value, tolerance, and
owner. The service verifies that the device exists, is active, and has no
unfinished case. A valid request creates a case in `awaiting_measurement`.

### 4. Record Measurement

A technician sends `POST /cases/{id}/measurements`. The case must be
`awaiting_measurement`. The service resolves the device room currently in
effect (the room of the most recent successful deployment), pins that room
onto the measurement, and computes the signed deviation from the nominal
value. The recorded room is an immutable fact of that measurement: a later
relocation changes the device room and the room stamped on subsequent
measurements, but never rewrites a recorded or sealed measurement. An
in-tolerance measurement moves the case to `review_ready`; an out-of-tolerance
measurement moves it to `recheck_required`.

### 5. Reopen Verification Case

The steward sends `POST /cases/{id}/reopen` after an out-of-tolerance result.
Only `recheck_required` cases may move back to `awaiting_measurement`. The
service records the actor and reason in the audit trail.

### 6. Seal Verification

The verifier sends `POST /cases/{id}/seal` after an accepted measurement. Only
`review_ready` cases may be sealed. The service selects the accepted
measurement with the smallest absolute deviation, creates exactly one evidence
seal, moves the case to `sealed`, and rejects later mutations.

## State And Rules

Device state:

- `active -> retired`
- `retired` is terminal.

VerificationCase state:

- `awaiting_measurement -> review_ready`
- `awaiting_measurement -> recheck_required`
- `recheck_required -> awaiting_measurement`
- `review_ready -> sealed`
- `sealed` is terminal.

Required invariants:

- Asset tags are unique after case-insensitive normalization.
- A device has at most one unfinished verification case.
- Measurements are accepted only while a case is awaiting measurement.
- A tolerance must be positive.
- A case can have at most one evidence seal.
- A sealed case cannot accept measurements, reopen, or seal again.
- Every mutation that changes a case also appends an audit event in the same
  repository commit.
- A measurement records the device room in effect at its submission time; that
  room is immutable and is never recomputed from the device's current room.
- A relocation only changes the room stamped on later measurements. It never
  alters prior measurements or evidence, including sealed ones.
- A relocation is all-or-nothing: device room, deployment record, and audit
  event commit together, and a rejected request leaves no trace.

## Modules

- `cmd/metrology-service`: process entrypoint and graceful HTTP shutdown.
- `cmd/workflowcheck`: bounded smoke checks that call the public HTTP API.
- `internal/httpapi`: routing, request decoding, and stable error mapping.
- `internal/assurance`: workflow orchestration and atomic decisions.
- `internal/domain`: entities, validation, state transitions, and errors.
- `internal/repository`: mutex-protected in-memory persistence with copy-on-write
  commits.
- `internal/contracts`: request and response contracts shared by the HTTP layer.

## Public Interfaces

- `GET /healthz`
- `POST /devices`
- `GET /devices/{id}`
- `POST /devices/{id}/deployments`
- `GET /devices/{id}/deployments`
- `POST /cases`
- `GET /cases/{id}`
- `POST /cases/{id}/measurements`
- `POST /cases/{id}/reopen`
- `POST /cases/{id}/seal`
- `GET /cases/{id}/events`

## Validation Plan

The baseline build command is `go build ./...`. Each declared workflow has a
bounded production smoke command under `cmd/workflowcheck`. The command starts
an HTTP test server in the same process, calls the public routes, and verifies
both success and failure behavior. The seal check also races two concurrent
seal requests to prove that the repository mutation lock permits exactly one
seal. The relocation check proves that a rejected relocation leaves the device
room, deployment history, and audit trail untouched and can be resubmitted,
that later measurements take the new room, and that recorded and sealed
measurements keep their original room.

Tests are intentionally deferred for this initialization baseline. The later
engineering-task stage will add unit and integration tests, including red/green
verification for the workflows and their failure paths.
