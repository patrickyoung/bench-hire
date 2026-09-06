Report net sales for 2026-09-01 through 2026-09-03, inclusive, from
inputs/sales.csv. Amounts are signed integer cents; negative rows are
refunds. Exclude rows outside the period.

Write work/requests/<this task's id>/totals.tsv, using literal tabs, with the
header `region\tnet_cents`, one row per region in alphabetical order, and a
final `TOTAL` row. Write RESULT.md beside it with the period, source, regional
and overall amounts in currency units, the largest region, and one useful
limitation of these records. Do not infer profit or growth without those inputs.

Run the task check after writing both files. It independently calculates the
totals. If it fails, diagnose the discrepancy and correct the deliverable.
