# Rails database migrations

When adding a database migration, use the Rails CLI generator:

    bundle exec rails g migration <DescriptiveName> [field:type ...]

Do not hand-author files under `db/migrate/`. The generator stamps a unique timestamp prefix on the filename; that prefix is the migration version key Rails uses to order and dedupe migrations. Hand-authored timestamps race with concurrent work on other branches and collide silently, leaving the schema in an order that depends on filesystem listing rather than commit order.

Edit the generated file in place after generation if the scaffold needs adjusting; do not rename it (renaming changes the version key).
