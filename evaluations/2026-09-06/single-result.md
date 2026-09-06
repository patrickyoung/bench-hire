Complete: five in-period GBP records produce a reconciled signed net total of GBP 320.00 across East, North, and South.

## Date range

- Requested period: 2026-09-01 through 2026-09-03 inclusive.
- Observed date range among included records: 2026-09-01 through 2026-09-03.
- The supplied records overall span 2026-08-31 through 2026-09-04.

## Currency and source

- Currency: GBP only; amounts were supplied as integer cents (100 cents per GBP).
- Source used: `../REQUEST.md`, specifically the seven inline CSV records in request `req-20260906-014319-6a31df04cca2`.
- No other records or reference data were used, and no currency conversion was performed.

## Signed net totals by region

| Region | Included calculation (cents) | Net cents | Net GBP |
|---|---:|---:|---:|
| East | 5,000 | 5,000 | GBP 50.00 |
| North | 12,000 + (-2,000) | 10,000 | GBP 100.00 |
| South | 8,000 + 9,000 | 17,000 | GBP 170.00 |
| **TOTAL** | **5,000 + 10,000 + 17,000** | **32,000** | **GBP 320.00** |

Negative amounts were preserved as signed refunds. South has the largest signed regional net amount at 17,000 cents (GBP 170.00).

## Reconciliation

- Included records: 5 of 7 supplied records.
- Direct included-record sum: 12,000 + 8,000 - 2,000 + 9,000 + 5,000 = 32,000 cents.
- Regional sum: East 5,000 + North 10,000 + South 17,000 = 32,000 cents.
- Reconciliation difference: 32,000 - 32,000 = 0 cents.
- The structured output at `requests/req-20260906-014319-6a31df04cca2/totals.tsv` contains the same region totals and total, in the requested order and units.

## Exclusions, missing information, and limitations

- Excluded as outside the requested period: `2026-08-31,North,GBP,50000` and `2026-09-04,East,GBP,7000`.
- No included record has a missing or nonnumeric date, region, currency, or amount.
- No region-missing or currency-missing records were present.
- Results are limited to the supplied inline records and represent signed net transaction amounts, not profit, growth, or broader business performance.

## Remaining undone

Nothing remains undone. All supplied records were assessed, the requested in-period GBP totals were calculated, one reconciliation pass was completed, and the requested TSV was written beside this report.
