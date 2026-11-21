# Process creator closure

A process sandbox keeps the original launcher's atomic pidfd and a private
completion pipe. Request version 3 reserves descriptor 7 for that pipe.
Mount sources start at descriptor 8. Versions 1 and 2 keep their original
descriptor layouts.

The namespace child closes the completion writer before setup. The native
creator reports completion only after its actual child has been joined. A
failed kill or wait supplies no completion. Copy publishers close the writer
before working and keep their own native exit checks.

The supervisor requires the exact completion byte, end of pipe and the
original launcher's pidfd exit. It also joins the original copy owner and its
publishers. A launcher exit, missing handoff or saved status cannot replace
these checks. An uncertain creator keeps its original handles and resources.
