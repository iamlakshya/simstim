# Auxiliary memory accounting

A protected `auxiliary_v1` leaf records one auxiliary reservation beside its primary manifest. It has its own boot ID, start time, token and complete declared unit cap. These are accounting data. They grant no stopped, boot, namespace, artifact or deletion authority. The primary's ordinary `run` and `life` stay unchanged.

The canonical auxiliary unit name is derived from the primary ID under the existing session slice. Callers cannot supply a unit path. Native reads use the same cgroup v2 object checks as primary accounting. An unknown or orphan branded auxiliary makes accounting unknown, rather than zero.

An auxiliary counts while its native unit exists, or for two minutes while an unlaunched reservation is fresh. A future start time counts conservatively. A live native unit must prove the complete declared cap and zero swap. The ordinary native unit must be absent. An active auxiliary then replaces the old no-unit primary starting charge, so the same operation counts once. The auxiliary boot ID is independent of the unchanged ordinary run's boot ID.

The data-only admission helper checks current room under the existing placement transaction. The real producer separately needs its original primary operation, genuine whole-stopped and native vacancy proof, and original artifact capture. It must publish the reservation atomically before native launch. At dispatch the observer counts the already-published auxiliary once, checks its exact identity and two-minute lifetime, and rechecks room. The real issuer must verify the created native cap and process placement before lending private boot or artifact rights.

Ordinary starts, restarts and dispatch refuse an uncleared auxiliary leaf or a live native auxiliary. A copied token or acquired lock cannot clear it. Only the original real controller may settle its actual native lifetime and clear the reservation. Purge and removal must preserve that ownership and reservation until native closure.

This change supplies accounting and native facts only. It does not register an auxiliary guest factory or make a stopped guest available. Those require the separate genuine native producer and its validation.
