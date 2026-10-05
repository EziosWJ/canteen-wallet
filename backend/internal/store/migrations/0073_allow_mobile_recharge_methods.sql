UPDATE sqlite_master SET sql = replace(sql,
    'CHECK (payment_method IN (''CASH'', ''BANK_TRANSFER'', ''OTHER''))',
    'CHECK (payment_method IN (''CASH'', ''BANK_TRANSFER'', ''WECHAT'', ''ALIPAY'', ''OTHER''))')
    WHERE type = 'table' AND name = 'recharge_receipts';
