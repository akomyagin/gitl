// Package config loads and validates gitl configuration.
//
// Планируемая модель (реализация — Этап 1+, см. docs/TECHNICAL_PLAN.md §5):
// два уровня конфига, сливаемых по приоритету
// флаг > env > .gitl.yaml (репо) > ~/.config/gitl/config.yaml (личный),
// через viper → struct Config. Пути — через os.UserConfigDir(), не хардкод.
// Пустой api_key → offline-режим с предупреждением в stderr (не падаем).
package config

// Заглушка Этапа 0: типы и Load() появятся в Этапе 1+.
