<?php

declare(strict_types=1);

namespace App\Logging;

use Monolog\Formatter\JsonFormatter;
use Monolog\Logger;
use Monolog\LogRecord;

/**
 * Monolog tap that enforces the mandatory structured-log fields (SPEC §12.4):
 *   ts, level, service, request_id, msg  (plus provider where applicable).
 *
 * Applied to the stderr channel so every line is JSON on stdout/stderr.
 * request_id is pulled from the per-request value stashed in RequestId, so
 * grep-by-request-id traceability works across the whole request.
 */
class JsonContextTap
{
    public function __invoke(Logger $logger): void
    {
        foreach ($logger->getHandlers() as $handler) {
            if (method_exists($handler, 'setFormatter')) {
                $handler->setFormatter(new JsonFormatter(JsonFormatter::BATCH_MODE_NEWLINES, true));
            }
        }

        $logger->pushProcessor(static function (LogRecord $record): LogRecord {
            $extra = $record->extra;
            $extra['service'] = 'core';
            $extra['ts'] = $record->datetime->format('Y-m-d\TH:i:s.vP');

            if (! isset($record->context['request_id']) && RequestId::current() !== null) {
                $context = $record->context;
                $context['request_id'] = RequestId::current();

                return $record->with(extra: $extra, context: $context);
            }

            return $record->with(extra: $extra);
        });
    }
}
