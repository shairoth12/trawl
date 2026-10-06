---
status: accepted
---

# Position merge is always on

CHA often produces several hits for one line of source: the real implementation and a generated mock living in the same package, or two backend calls (`QueryRowContext` and `Scan`) inside one dependency method. Reporting each separately gave two records for the same line, and `--dedup` could not remove them because it compares function names, which differ. `mergeByPosition` now keeps one record per (service type, source position): the higher confidence wins, and on a tie the interface name (`Store.Get`) wins over the concrete method name (`(*sqlStore).Get`). It runs before `--dedup` and is not a flag, because two records for one line and one service are never useful output.
