# Placement descriptions

A new primary session can carry a version 1 placement description. It says whether the original client chose the host automatically or explicitly, and gives one short reason. The host validates and owns that data before preparing the new session.

Both primary start paths copy the original description into `placed_by` and `place_reason` before their first manifest publication. Repository preparation does not replace it. A clone retains these immutable strings by value. The list view cleans and bounds them for display. A member does not claim its primary's host selection.

The description is advisory metadata. A custom SDK can supply a label. It grants no capacity, session operation, runtime, Need or native start permission. The actual selected host must still perform fresh admission under its placement lock and check its original runtime and native boot sources.
