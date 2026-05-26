-- +goose Up
INSERT INTO children (name) VALUES ('Демид'), ('Егор');

INSERT INTO activities (name) VALUES
    ('Гимнастика'),
    ('Логопед'),
    ('Тренажор'),
    ('Психолог'),
    ('Английский');

-- +goose Down
DELETE FROM activities;
DELETE FROM children;
