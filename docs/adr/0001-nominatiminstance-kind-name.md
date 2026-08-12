# Name the API kind NominatimInstance

The managed install is **NominatimInstance** in ubiquitous language; the custom resource kind is still `Nominatim`, which invites calling the install “Nominatim” and colliding with upstream. We will rename the kind to `NominatimInstance` (and align shortNames/docs) while there are no external users, rather than keep a permanent glossary≠API split or delay until a breaking migration is costly.
