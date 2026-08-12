# Nominatim on Kubernetes

Domain language for running upstream Nominatim as a managed install in Kubernetes — lifecycle, data coverage, and serving — not general Kubernetes or Postgres vocabulary.

## Language

### Install

**Nominatim**:
The upstream OpenStreetMap geocoding software (CLI, HTTP API, schema, and replication tooling).
_Avoid_: Using this word alone for a managed install in the cluster

**NominatimInstance**:
One managed install of Nominatim in Kubernetes: desired regions, database attachment, serving, and day-2 work for that install.
_Avoid_: Nominatim (alone), instance, deployment, cluster (when meaning the install)

**Operation**:
One finite workflow against a NominatimInstance (bootstrap, add regions, update, admin work, and the like).
_Avoid_: Job, workflow (when meaning this unit of work)

### Postgres

**PostgresCluster**:
The Postgres HA unit attached to a NominatimInstance.
_Avoid_: Cluster (alone), database cluster (when meaning this unit)

**PostgresInstance**:
One Postgres server process/pod inside a PostgresCluster (primary or standby).
_Avoid_: Instance (alone), replica

**NominatimDatabase**:
Nominatim’s application data and schema inside a PostgresCluster (search/placex and related objects).
_Avoid_: Database (alone), Postgres database (when meaning this application data)

### Coverage

**Region**:
A Geofabrik-style coverage unit (path such as `europe/monaco`) that a NominatimInstance may desire or have imported.
_Avoid_: Extract, coverage (as a synonym for a single unit)

**Update**:
Applying OSM diffs to an already-bootstrapped NominatimInstance (typically via Geofabrik/pyosmium).
_Avoid_: Replication (when meaning this activity)

**CatchUp**:
An Operation that Updates a NominatimInstance until diff apply is idle.
_Avoid_: Using Update alone when you mean “run until idle”

**Replication**:
Upstream Nominatim’s replication configuration and lag (`NOMINATIM_REPLICATION_*` and related), not our per-region diff cursor.
_Avoid_: Using Replication for Update progress or SequenceState

**SequenceState**:
Per-region cursor for Update progress (`sequenceNumber@timestamp`), distinct from Replication lag.
_Avoid_: Replication lag, replication cursor (when meaning this cursor)

**AuxData**:
Optional non-region datasets that enrich a NominatimDatabase (for example Wikipedia importance or postcodes).
_Avoid_: Extra data, secondary data (as synonyms)

### Work

**Bootstrap**:
The Operation that first builds a NominatimDatabase for a NominatimInstance from regions or PBF.
_Avoid_: Import (when meaning this Operation)

**AddRegions**:
The Operation that Imports missing Regions into an existing NominatimDatabase without wiping it.
_Avoid_: AddData (when meaning this Operation)

**Rebuild**:
The Operation that wipes and rebuilds a NominatimDatabase (and related install state) when coverage or import config must be resealed.
_Avoid_: Reimport, Bootstrap (when meaning this day-2 rebuild)

**Import**:
The upstream Nominatim CLI action that loads OSM data into a NominatimDatabase.
_Avoid_: Bootstrap (when meaning the CLI action alone)

**Freeze**:
The Operation that drops OSM-diff tables so the NominatimInstance can serve but no longer Update from diffs.
_Avoid_: Read-only mode (vague); serving continues, Update/AddRegions that need update structures fail afterward

**Worker**:
The process that runs Nominatim CLI for an Operation.
_Avoid_: Using Worker for Gunicorn/HTTP pool size (say API worker if you must name it)

### Serving

**API**:
The read-only Nominatim HTTP service for a NominatimInstance.
_Avoid_: Using API alone when you mean the Kubernetes API

**UI**:
The optional web interface served alongside the API for a NominatimInstance.
_Avoid_: Dashboard, frontend (when meaning this component)

### Storage

**Project**:
The durable Nominatim working directory for a NominatimInstance (markers, local files, not the NominatimDatabase).
_Avoid_: ProjectVolume, data directory (when meaning this)

**Flatnode**:
Optional osm2pgsql flatnode store for a NominatimInstance, used while Workers Import or Update — not by the API.
_Avoid_: Flat nodes volume (as a vague synonym)
