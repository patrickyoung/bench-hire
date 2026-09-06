Extract the distinct maintenance requests from inputs/messages.txt.
Treat M-103 as an explicit correction to M-101, not another task. Keep source
message IDs so the manager can verify the extraction.

Write actions.tsv beside this task's RESULT.md. Use literal tabs and the exact
columns `request_id`, `owner`, `due`, `action`, `sources`. Order rows by the first
message ID. Use ISO dates when supplied and `unknown` for an unstated date. Use
the short action labels `Renew pump service` and `Request valve quote` so this
example's check can compare the extraction without judging prose. Join multiple
source IDs with commas.

In RESULT.md, explain the date correction and ask the manager for the missing
deadline. Do not contact anyone or claim the actions are completed. Run the
task check and address any mismatch with the source.
