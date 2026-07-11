-- sorm migration: add_priority
-- modify "tasks" table
ALTER TABLE "public"."tasks" ADD COLUMN "priority" integer NOT NULL;
