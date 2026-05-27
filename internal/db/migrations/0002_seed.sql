-- +goose Up
INSERT INTO persons (name, kind) VALUES ('Демид', 'child'), ('Егор', 'child');

-- +goose Down
DELETE FROM persons;
