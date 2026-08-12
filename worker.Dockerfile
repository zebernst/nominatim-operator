# nominatim-worker — Nominatim CLI + import/update tooling for NominatimOperations
# Base: Ubuntu 24.04 (matches upstream Nominatim install docs).
# Install: apt osm2pgsql + PyPI nominatim-db (official packaging) — NOT mediagis/nominatim.
# Talks to external Postgres via PG* / NOMINATIM_DATABASE_DSN. No in-image Postgres server.
ARG NOMINATIM_VERSION=5.3.2

FROM ubuntu:24.04

ARG NOMINATIM_VERSION
ENV DEBIAN_FRONTEND=noninteractive \
    PROJECT_DIR=/nominatim \
    IMPORT_STAGING=/import-staging \
    PATH=/opt/nominatim/venv/bin:/usr/lib/python3-pyosmium:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    PIP_DISABLE_PIP_VERSION_CHECK=1 \
    PYTHONDONTWRITEBYTECODE=1

# osm2pgsql + client tools for external DB.
# python3-pyosmium from apt (PyPI wheels are unreliable on aarch64); expose via system-site-packages.
RUN apt-get update -qq \
    && apt-get install -y --no-install-recommends \
        build-essential \
        ca-certificates \
        curl \
        gosu \
        libicu-dev \
        libicu74 \
        osm2pgsql \
        pkg-config \
        postgresql-client \
        python3 \
        python3-dev \
        python3-icu \
        python3-pip \
        python3-pyosmium \
        python3-venv \
    && python3 -m venv --system-site-packages /opt/nominatim/venv \
    && /opt/nominatim/venv/bin/pip install --no-cache-dir --upgrade pip \
    && /opt/nominatim/venv/bin/pip install --no-cache-dir \
        "psycopg[binary]" \
        "nominatim-db==${NOMINATIM_VERSION}" \
    && apt-get purge -y --auto-remove build-essential libicu-dev pkg-config python3-dev \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

RUN useradd --system --create-home --home-dir /srv/nominatim --shell /usr/sbin/nologin nominatim \
    && mkdir -p "${PROJECT_DIR}" "${IMPORT_STAGING}" \
    && chown -R nominatim:nominatim /srv/nominatim "${PROJECT_DIR}" "${IMPORT_STAGING}"

COPY images/worker/env.defaults /opt/nominatim/env.defaults
COPY images/worker/scripts/*.sh /opt/nominatim/scripts/
RUN chmod 0755 /opt/nominatim/scripts/*.sh

WORKDIR /nominatim
# Entrypoint dispatches OPERATION_TYPE (Bootstrap|AddRegions|Reimport|Update|CatchUp|Refresh|Migrate|Freeze).
# Root so Jobs can fix volume ownership; CLI runs as nominatim via gosu.
USER root
ENTRYPOINT ["/opt/nominatim/scripts/entrypoint.sh"]
