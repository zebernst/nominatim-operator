# Name the wipe-and-rebuild Operation Rebuild

Day-2 wipe-and-rebuild of a NominatimDatabase is **Rebuild** in ubiquitous language; the API still calls that Operation (and the matching region-change policy value) `Reimport`, which reads like a second Bootstrap and collides with “import” talk. We will rename those API strings to `Rebuild` while there are no external users, so glossary and API stay aligned—same rationale as ADR-0001 for NominatimInstance.
